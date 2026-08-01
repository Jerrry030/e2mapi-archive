package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"e2m.local/contracts"
)

// WebhookNotifier delivers an event to a generic HTTP webhook as a small JSON
// document. Unlike Feishu/QQ, the destination URL is per-route (carried in the
// NotificationRoute.TargetRef), so the notifier exposes SendTo(url, ev) and the
// router supplies the resolved URL. This is the "webhook" channel that already
// exists in the contracts/UI but previously had no implementation.
type WebhookNotifier struct {
	http   *http.Client
	policy webhookURLPolicy
}

var _ Notifier = (*WebhookNotifier)(nil)

func NewWebhook() *WebhookNotifier {
	return newWebhook(webhookURLPolicy{}, net.DefaultResolver)
}

// SafeNotificationHTTPClient applies the same no-proxy, no-redirect and
// public-address-only transport policy to other user-controlled channels.
func SafeNotificationHTTPClient(timeout time.Duration) *http.Client {
	client := newWebhook(webhookURLPolicy{}, net.DefaultResolver).http
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}

type webhookURLPolicy struct {
	allowHTTP            bool
	allowPrivateNetworks bool
}

type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

func newWebhook(policy webhookURLPolicy, resolver ipResolver) *WebhookNotifier {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Webhook routes are user-managed. Never let an environment proxy turn an
	// otherwise public URL into a connection to a privileged internal network.
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("webhook: invalid destination address: %w", err)
		}
		ips, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("webhook: resolve destination: %w", err)
		}
		if len(ips) == 0 {
			return nil, errors.New("webhook: destination has no IP addresses")
		}
		for _, resolved := range ips {
			if err := validateWebhookIP(resolved.IP, policy.allowPrivateNetworks); err != nil {
				return nil, err
			}
		}
		var dialErr error
		for _, resolved := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = err
		}
		return nil, fmt.Errorf("webhook: connect destination: %w", dialErr)
	}
	return &WebhookNotifier{
		http: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		policy: policy,
	}
}

// ValidateWebhookURL applies the policy used when notification routes are
// created or updated. Delivery repeats this validation and verifies every
// resolved address immediately before dialing.
func ValidateWebhookURL(raw string) error {
	return validateWebhookURL(raw, webhookURLPolicy{})
}

func validateWebhookURL(raw string, policy webhookURLPolicy) error {
	trimmed := strings.TrimSpace(raw)
	if strings.Contains(trimmed, "#") {
		return errors.New("must not include a fragment")
	}
	parsed, err := url.ParseRequestURI(trimmed)
	if err != nil {
		return errors.New("must be a valid absolute URL")
	}
	if parsed.Scheme != "https" && !(policy.allowHTTP && parsed.Scheme == "http") {
		return errors.New("must use HTTPS")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("must include a destination host")
	}
	if parsed.User != nil {
		return errors.New("must not include URL credentials")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("must not target localhost")
	}
	if ip := net.ParseIP(host); ip != nil {
		if err := validateWebhookIP(ip, policy.allowPrivateNetworks); err != nil {
			return err
		}
	}
	return nil
}

var nonPublicWebhookPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	// Translation and tunneling ranges can carry an otherwise blocked IPv4
	// destination through an IPv6-looking URL. Deny the complete mechanism,
	// rather than trying to decode only the common embedded-address layouts.
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3ffe::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

func validateWebhookIP(ip net.IP, allowPrivate bool) error {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return errors.New("webhook: destination resolved to an invalid IP address")
	}
	addr = addr.Unmap()
	if allowPrivate {
		return nil
	}
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return fmt.Errorf("webhook: destination IP %s is not public", addr)
	}
	for _, prefix := range nonPublicWebhookPrefixes {
		if prefix.Contains(addr) {
			return fmt.Errorf("webhook: destination IP %s is not public", addr)
		}
	}
	return nil
}

func (w *WebhookNotifier) Channel() contracts.NotificationChannel {
	return contracts.NotificationChannelWebhook
}

// Send satisfies Notifier for completeness; the URL is expected in
// ev.Fields["webhookURL"]. The router normally calls SendTo directly.
func (w *WebhookNotifier) Send(ctx context.Context, ev Event) error {
	url := ""
	if ev.Fields != nil {
		url = ev.Fields["webhookURL"]
	}
	if url == "" {
		return fmt.Errorf("webhook: no target url")
	}
	return w.SendTo(ctx, url, ev)
}

// webhookPayload is the JSON body posted to a generic webhook. It carries both
// the rendered message and the structured fields so downstream automations
// (n8n, Zapier, a custom endpoint) can branch on them.
type webhookPayload struct {
	Title      string            `json:"title,omitempty"`
	Text       string            `json:"text"`
	Message    string            `json:"message"`
	EventLevel string            `json:"event_level,omitempty"`
	RiskLevel  string            `json:"risk_level,omitempty"`
	Result     string            `json:"result,omitempty"`
	UserID     int64             `json:"user_id,omitempty"`
	InstanceID string            `json:"instance_id,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
	At         time.Time         `json:"at"`
}

// SendTo posts the event to an explicit webhook URL.
func (w *WebhookNotifier) SendTo(ctx context.Context, targetURL string, ev Event) error {
	if err := validateWebhookURL(targetURL, w.policy); err != nil {
		return fmt.Errorf("webhook: invalid target URL: %w", err)
	}
	payload := webhookPayload{
		Title:      ev.Title,
		Text:       ev.Text,
		Message:    ev.Message(),
		EventLevel: string(ev.EventLevel),
		RiskLevel:  string(ev.RiskLevel),
		Result:     ev.Result,
		UserID:     ev.UserID,
		InstanceID: ev.InstanceID,
		Fields:     ev.Fields,
		At:         time.Now().UTC(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.http.Do(req)
	if err != nil {
		// net/http wraps transport failures in url.Error, whose Error string
		// contains the complete webhook URL. Tokens commonly live in the path or
		// query, so never let that wrapper cross the Vault delivery boundary.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("webhook: request failed: %w", urlErr.Err)
		}
		return errors.New("webhook: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
	}
	return nil
}
