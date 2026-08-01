package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/vault"
)

func fixedNow() time.Time { return time.Unix(1782000000, 0) }

func TestFeishuSendWithSignature(t *testing.T) {
	var gotSign, gotTS string
	var gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotSign, _ = body["sign"].(string)
		gotTS, _ = body["timestamp"].(string)
		if c, ok := body["content"].(map[string]any); ok {
			gotText, _ = c["text"].(string)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
	}))
	defer srv.Close()

	f := NewFeishu(srv.URL, "my-secret")
	f.now = fixedNow
	if err := f.Send(context.Background(), Event{Title: "T", Text: "body"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotSign == "" || gotTS == "" {
		t.Fatal("expected signed payload")
	}
	if gotText != "T\nbody" {
		t.Fatalf("unexpected text: %q", gotText)
	}
}

func TestRouterRespectsMinRiskLevel(t *testing.T) {
	var sent int32
	fake := &fakeNotifier{ch: contracts.NotificationChannelFeishu, sent: &sent}
	r := NewRouter(fake, nil)

	route := contracts.NotificationRoute{Enabled: true, Channel: contracts.NotificationChannelFeishu, TargetRef: "system:feishu", MinRiskLevel: contracts.RiskLevelL2}
	// L1 event below threshold -> not sent
	r.Dispatch(context.Background(), Event{RiskLevel: contracts.RiskLevelL1}, route)
	if atomic.LoadInt32(&sent) != 0 {
		t.Fatal("L1 should not pass L2 route")
	}
	// L3 event above threshold -> sent
	r.Dispatch(context.Background(), Event{RiskLevel: contracts.RiskLevelL3}, route)
	if atomic.LoadInt32(&sent) != 1 {
		t.Fatal("L3 should pass L2 route")
	}
}

func TestRouterDispatchesOnlySelectedSystemChannel(t *testing.T) {
	var qqSent int32
	var feishuSent int32
	feishu := &fakeNotifier{ch: contracts.NotificationChannelFeishu, sent: &feishuSent}
	qq := &fakeNotifier{ch: contracts.NotificationChannelQQ, sent: &qqSent}
	r := NewRouter(feishu, qq)
	route := contracts.NotificationRoute{Enabled: true, Channel: contracts.NotificationChannelQQ, TargetRef: "system:qq", MinRiskLevel: contracts.RiskLevelL1}
	r.Dispatch(context.Background(), Event{RiskLevel: contracts.RiskLevelL1}, route)
	if atomic.LoadInt32(&qqSent) != 1 {
		t.Fatal("QQ route was not delivered")
	}
	if atomic.LoadInt32(&feishuSent) != 0 {
		t.Fatal("QQ route must not also deliver to Feishu")
	}
}

type fakeNotifier struct {
	ch   contracts.NotificationChannel
	fail bool
	sent *int32
}

func (f *fakeNotifier) Channel() contracts.NotificationChannel { return f.ch }
func (f *fakeNotifier) Send(_ context.Context, _ Event) error {
	if f.fail {
		return errFake
	}
	atomic.AddInt32(f.sent, 1)
	return nil
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "fake" }
func TestRouterDispatchesWebhookRouteToTargetRef(t *testing.T) {
	var got struct {
		Title      string            `json:"title"`
		Text       string            `json:"text"`
		Message    string            `json:"message"`
		RiskLevel  string            `json:"risk_level"`
		Result     string            `json:"result"`
		UserID     int64             `json:"user_id"`
		InstanceID string            `json:"instance_id"`
		Fields     map[string]string `json:"fields"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("webhook method = %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("content type = %q", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode webhook payload: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	r := NewRouter(nil, nil)
	r.SetWebhook(newWebhook(webhookURLPolicy{
		allowHTTP:            true,
		allowPrivateNetworks: true,
	}, net.DefaultResolver))
	v := vault.NewMemoryVault()
	const targetRef = "credential_ref:user/101/notification/automation"
	if _, err := v.Store(context.Background(), targetRef, srv.URL); err != nil {
		t.Fatalf("store webhook target: %v", err)
	}
	r.SetSecretResolver(v)
	r.Dispatch(context.Background(), Event{
		UserID: 101, InstanceID: "inst-a", RiskLevel: contracts.RiskLevelL2, Result: "accepted",
		Title: "Reconcile", Text: "created=1", Fields: map[string]string{"planId": "plan-a"},
	}, contracts.NotificationRoute{
		UserID: 101, Enabled: true, Channel: contracts.NotificationChannelWebhook, TargetRef: targetRef, MinRiskLevel: contracts.RiskLevelL1,
	})

	if got.UserID != 101 || got.InstanceID != "inst-a" || got.RiskLevel != string(contracts.RiskLevelL2) || got.Result != "accepted" {
		t.Fatalf("wrong webhook envelope: %+v", got)
	}
	if got.Message != "Reconcile\ncreated=1" || got.Fields["planId"] != "plan-a" {
		t.Fatalf("wrong webhook message/fields: %+v", got)
	}
}

func TestValidateWebhookURLRejectsUnsafeTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
	}{
		{name: "plain HTTP", target: "http://hooks.example.com/events"},
		{name: "loopback IPv4", target: "https://127.0.0.1/events"},
		{name: "private IPv4", target: "https://10.20.30.40/events"},
		{name: "metadata IPv4", target: "https://169.254.169.254/latest/meta-data"},
		{name: "loopback IPv6", target: "https://[::1]/events"},
		{name: "mapped loopback IPv6", target: "https://[::ffff:127.0.0.1]/events"},
		{name: "IPv4-translatable loopback", target: "https://[::ffff:0:7f00:1]/events"},
		{name: "localhost", target: "https://api.localhost/events"},
		{name: "credentials", target: "https://user:pass@hooks.example.com/events"},
		{name: "fragment", target: "https://hooks.example.com/events#internal"},
		{name: "local NAT64", target: "https://[64:ff9b:1:fffe::7f00:1]/events"},
		{name: "well-known NAT64", target: "https://[64:ff9b::a00:8]/events"},
		{name: "6to4", target: "https://[2002:0a00:0008::1]/events"},
		{name: "site-local IPv6", target: "https://[fec0::1]/events"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateWebhookURL(tt.target); err == nil {
				t.Fatalf("ValidateWebhookURL(%q) succeeded", tt.target)
			}
		})
	}
	if err := ValidateWebhookURL("https://hooks.example.com/events?source=e2m"); err != nil {
		t.Fatalf("public HTTPS target rejected: %v", err)
	}
}

func TestValidateNotificationTargetRef(t *testing.T) {
	t.Parallel()
	if err := ValidateNotificationTargetRef("credential_ref:user/42/notification/ops", 42); err != nil {
		t.Fatalf("owner-scoped ref rejected: %v", err)
	}
	for _, ref := range []string{
		"https://hooks.example.com/events",
		"credential_ref:user/43/notification/ops",
		"credential_ref:user/42/upstream/ops",
		"credential_ref:user/42/notification/",
		"credential_ref:user/42/notification/ops/child",
		"credential_ref:user/42/notification/ops%2fchild",
		"credential_ref:user/42/notification/ops child",
	} {
		if err := ValidateNotificationTargetRef(ref, 42); err == nil {
			t.Fatalf("unsafe notification ref %q accepted", ref)
		}
	}
}

func TestRouterResolvesRotatedWebhookTargetAtDispatch(t *testing.T) {
	t.Parallel()
	var firstCalls, secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer second.Close()

	const ref = "credential_ref:user/7/notification/ops"
	v := vault.NewMemoryVault()
	_, _ = v.Store(context.Background(), ref, first.URL)
	r := NewRouter(nil, nil)
	r.SetSecretResolver(v)
	r.SetWebhook(newWebhook(webhookURLPolicy{allowHTTP: true, allowPrivateNetworks: true}, net.DefaultResolver))
	route := contracts.NotificationRoute{
		UserID: 7, Enabled: true, Channel: contracts.NotificationChannelWebhook,
		TargetRef: ref, MinRiskLevel: contracts.RiskLevelL0,
	}
	r.Dispatch(context.Background(), Event{}, route)
	_, _ = v.Store(context.Background(), ref, second.URL)
	r.Dispatch(context.Background(), Event{}, route)
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("dispatch calls: first=%d second=%d", firstCalls.Load(), secondCalls.Load())
	}
}

func TestWebhookRejectsMixedPublicAndPrivateDNSResults(t *testing.T) {
	t.Parallel()
	n := newWebhook(webhookURLPolicy{}, staticIPResolver{ips: []net.IPAddr{
		{IP: net.ParseIP("93.184.216.34")},
		{IP: net.ParseIP("10.0.0.8")},
	}})
	err := n.SendTo(context.Background(), "https://hooks.example.com/events", Event{Text: "test"})
	if err == nil || !strings.Contains(err.Error(), "is not public") {
		t.Fatalf("mixed DNS result error = %v", err)
	}
}

type staticIPResolver struct {
	ips []net.IPAddr
	err error
}

func (r staticIPResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.ips, r.err
}

func TestWebhookRejectsPrivateDNSResolution(t *testing.T) {
	t.Parallel()
	n := newWebhook(webhookURLPolicy{}, staticIPResolver{
		ips: []net.IPAddr{{IP: net.ParseIP("192.168.1.20")}},
	})
	err := n.SendTo(context.Background(), "https://hooks.example.com/events", Event{Text: "test"})
	if err == nil || !strings.Contains(err.Error(), "is not public") {
		t.Fatalf("SendTo private DNS result error = %v", err)
	}
}

func TestWebhookDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, destination.URL, http.StatusFound)
	}))
	defer source.Close()

	n := newWebhook(webhookURLPolicy{
		allowHTTP:            true,
		allowPrivateNetworks: true,
	}, net.DefaultResolver)
	err := n.SendTo(context.Background(), source.URL, Event{Text: "test"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("SendTo redirect error = %v", err)
	}
	if got := redirected.Load(); got != 0 {
		t.Fatalf("redirect destination received %d requests", got)
	}
}

func TestWebhookResolverFailureIsReturned(t *testing.T) {
	t.Parallel()
	want := errors.New("resolver unavailable")
	n := newWebhook(webhookURLPolicy{}, staticIPResolver{err: want})
	err := n.SendTo(context.Background(), "https://hooks.example.com/events", Event{Text: "test"})
	if err == nil || !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("SendTo resolver error = %v", err)
	}
}

func TestWebhookTransportErrorDoesNotLeakTargetURL(t *testing.T) {
	t.Parallel()
	const secretURL = "https://hooks.example.com/private-token?key=top-secret"
	n := newWebhook(webhookURLPolicy{}, staticIPResolver{err: errors.New("resolver unavailable")})
	err := n.SendTo(context.Background(), secretURL, Event{Text: "test"})
	if err == nil {
		t.Fatal("SendTo unexpectedly succeeded")
	}
	for _, secret := range []string{"private-token", "top-secret", secretURL} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("transport error leaked %q: %v", secret, err)
		}
	}
}
