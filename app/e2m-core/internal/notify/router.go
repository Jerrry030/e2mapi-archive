package notify

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"e2m.local/contracts"
	"e2m.local/core/internal/vault"
)

// Router dispatches an event to the channel selected by each route. Operators
// can configure separate Feishu and QQ routes to get dual delivery without one
// route unexpectedly sending to a different channel.
type Router struct {
	primary    Notifier         // Feishu (may be nil if unconfigured)
	secondary  Notifier         // QQ (may be nil)
	webhook    *WebhookNotifier // generic per-route webhook (always available)
	secrets    SecretResolver   // resolves per-user notification refs just in time
	deliveries DeliveryStore
}

type DeliveryStore interface {
	CreateNotificationDelivery(context.Context, contracts.NotificationDelivery) (contracts.NotificationDelivery, error)
}

// SecretResolver is the narrow portion of Vault needed during notification
// delivery. Plaintext target URLs never leave this dispatch boundary.
type SecretResolver interface {
	Resolve(ctx context.Context, ref string) (vault.Secret, error)
}

func NewRouter(primary, secondary Notifier) *Router {
	return &Router{primary: primary, secondary: secondary, webhook: NewWebhook()}
}

// SetWebhook overrides the generic webhook notifier (tests inject a fake).
func (r *Router) SetWebhook(w *WebhookNotifier) { r.webhook = w }

// SetSecretResolver wires the Vault used for per-route notification targets.
func (r *Router) SetSecretResolver(resolver SecretResolver) { r.secrets = resolver }

// SetDeliveryStore turns Dispatch into a durable enqueue operation. It is set
// by Core in both memory and PostgreSQL modes before any producer starts.
func (r *Router) SetDeliveryStore(deliveries DeliveryStore) { r.deliveries = deliveries }

func (r *Router) ChannelConfigured(channel contracts.NotificationChannel) bool {
	switch channel {
	case contracts.NotificationChannelFeishu:
		return r.primary != nil
	case contracts.NotificationChannelQQ:
		return r.secondary != nil
	case contracts.NotificationChannelWebhook:
		return r.webhook != nil && r.secrets != nil
	default:
		return false
	}
}

func (r *Router) RouteConfigured(ctx context.Context, route contracts.NotificationRoute) bool {
	if IsPersonalNotificationTargetRef(route.TargetRef, route.UserID, route.Channel) {
		if r.secrets == nil {
			return false
		}
		secret, err := r.secrets.Resolve(ctx, strings.TrimSpace(route.TargetRef))
		if err != nil {
			return false
		}
		credential, err := DecodePersonalTargetCredential(secret.Value)
		return err == nil && credential.Channel == route.Channel
	}
	if route.Channel == contracts.NotificationChannelWebhook {
		return r.webhook != nil && r.secrets != nil
	}
	if !IsSystemNotificationTargetRef(route.TargetRef, route.Channel) {
		return false
	}
	return r.ChannelConfigured(route.Channel)
}

// ValidateNotificationTargetRef verifies that ref is a notification secret
// owned by userID. Keeping this check beside dispatch makes persisted or legacy
// rows fail closed even if they bypassed the HTTP API.
func ValidateNotificationTargetRef(ref string, userID int64) error {
	if userID <= 0 {
		return fmt.Errorf("requires a valid owner")
	}
	ref = strings.TrimSpace(ref)
	prefix := "credential_ref:user/" + strconv.FormatInt(userID, 10) + "/notification/"
	name, ok := strings.CutPrefix(ref, prefix)
	if !ok || name == "" {
		return fmt.Errorf("must reference an owner-scoped notification credential")
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._-", char) {
			continue
		}
		return fmt.Errorf("must reference an owner-scoped notification credential")
	}
	return nil
}

// RenderTemplate fills route-template placeholders from the event. Unknown
// placeholders are left as-is so typos are visible in the delivered message.
func RenderTemplate(tmpl string, ev Event) string {
	if strings.TrimSpace(tmpl) == "" {
		return ev.Title + "\n" + ev.Text
	}
	pairs := []string{
		"{title}", ev.Title,
		"{text}", ev.Text,
		"{riskLevel}", string(ev.RiskLevel),
		"{eventLevel}", string(ev.EventLevel),
		"{result}", ev.Result,
		"{userId}", strconv.FormatInt(ev.UserID, 10),
		"{instanceId}", ev.InstanceID,
	}
	for k, v := range ev.Fields {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(tmpl)
}

// PrepareDelivery applies route filtering and produces the durable, non-secret
// event snapshot. Test sends may set force=true to bypass enabled/level gates.
func PrepareDelivery(ev Event, route contracts.NotificationRoute, kind contracts.NotificationDeliveryKind, force bool) (contracts.NotificationDelivery, bool) {
	if !ev.EventLevel.Valid() {
		if strings.TrimSpace(ev.Result) == "" {
			ev.EventLevel = contracts.EventLevel(ev.RiskLevel)
		} else {
			ev.EventLevel = contracts.DefaultEventLevel(ev.RiskLevel, ev.Result)
		}
	}
	if !force && (!route.Enabled || !MeetsMinEvent(ev.EventLevel, route.EffectiveMinEventLevel())) {
		return contracts.NotificationDelivery{}, false
	}
	return contracts.NotificationDelivery{
		UserID: route.UserID, RouteID: route.ID, RouteName: route.Name, TargetRef: route.TargetRef,
		Template: route.Template, Channel: route.Channel, Kind: kind, Status: contracts.NotificationDeliveryPending,
		EventLevel: ev.EventLevel, RiskLevel: ev.RiskLevel, Result: ev.Result, InstanceID: ev.InstanceID,
		Title: ev.Title, Text: ev.Text, Fields: ev.Fields, MaxAttempts: 5,
	}, true
}

// EnqueueRoute persists one delivery. The caller may safely return after this;
// the background worker owns all network I/O and retry state.
func (r *Router) EnqueueRoute(ctx context.Context, ev Event, route contracts.NotificationRoute, kind contracts.NotificationDeliveryKind, force bool) (contracts.NotificationDelivery, bool, error) {
	delivery, ok := PrepareDelivery(ev, route, kind, force)
	if !ok {
		return contracts.NotificationDelivery{}, false, nil
	}
	if r.deliveries == nil {
		return contracts.NotificationDelivery{}, false, fmt.Errorf("notification delivery store is not configured")
	}
	created, err := r.deliveries.CreateNotificationDelivery(ctx, delivery)
	return created, err == nil, err
}

// Dispatch enqueues ev, respecting the route's minimum event level. Enqueue
// errors are logged and never fail the domain operation that raised the alert.
func (r *Router) Dispatch(ctx context.Context, ev Event, route contracts.NotificationRoute) {
	// Keep deliberately constructed routers usable in focused tests and small
	// embeddings. Core always wires a delivery store, so production takes the
	// durable path below.
	if r.deliveries == nil {
		delivery, ok := PrepareDelivery(ev, route, contracts.NotificationDeliveryKindEvent, false)
		if !ok {
			return
		}
		if err := r.SendDelivery(ctx, delivery); err != nil {
			log.Printf("notify: direct send failed: %v", err)
		}
		return
	}
	if _, _, err := r.EnqueueRoute(ctx, ev, route, contracts.NotificationDeliveryKindEvent, false); err != nil {
		log.Printf("notify: enqueue failed: %v", err)
	}
}

// DispatchAll sends ev over every matching route.
func (r *Router) DispatchAll(ctx context.Context, ev Event, routes []contracts.NotificationRoute) {
	for _, route := range routes {
		r.Dispatch(ctx, ev, route)
	}
}

// SendDelivery performs exactly one external attempt and returns a safe typed
// error to the worker. Credentials are resolved just in time.
func (r *Router) SendDelivery(ctx context.Context, d contracts.NotificationDelivery) error {
	ev := Event{UserID: d.UserID, InstanceID: d.InstanceID, EventLevel: d.EventLevel, RiskLevel: d.RiskLevel,
		Result: d.Result, Title: d.Title, Text: d.Text, Fields: d.Fields}
	if strings.TrimSpace(d.Template) != "" {
		ev.Text = RenderTemplate(d.Template, ev)
		ev.Title = ""
	}
	if d.Channel == contracts.NotificationChannelWebhook {
		if r.webhook == nil || r.secrets == nil {
			return Permanent("channel_unconfigured", "Webhook 发送服务未配置")
		}
		if err := ValidateNotificationTargetRef(d.TargetRef, d.UserID); err != nil {
			return Permanent("invalid_target", "Webhook 地址引用无效")
		}
		target, err := r.secrets.Resolve(ctx, strings.TrimSpace(d.TargetRef))
		if err != nil {
			return Permanent("credential_not_found", "Webhook 地址不可用")
		}
		if err := r.webhook.SendTo(ctx, strings.TrimSpace(target.Value), ev); err != nil {
			return Classify(err)
		}
		return nil
	}
	if IsPersonalNotificationTargetRef(d.TargetRef, d.UserID, d.Channel) {
		if r.secrets == nil {
			return Permanent("channel_unconfigured", "Personal notification credential is unavailable")
		}
		secret, err := r.secrets.Resolve(ctx, strings.TrimSpace(d.TargetRef))
		if err != nil {
			return Permanent("credential_not_found", "Personal notification credential is unavailable")
		}
		credential, err := DecodePersonalTargetCredential(secret.Value)
		if err != nil || credential.Channel != d.Channel {
			return Permanent("invalid_credential", "Personal notification credential is invalid")
		}
		var sender Notifier
		switch credential.Channel {
		case contracts.NotificationChannelFeishu:
			sender = NewPersonalFeishu(credential.WebhookURL, credential.SigningSecret)
		case contracts.NotificationChannelQQ:
			sender = NewPersonalQQ(credential.OneBotURL, credential.AccessToken, credential.GroupID)
		}
		if sender == nil {
			return Permanent("invalid_credential", "Personal notification credential is invalid")
		}
		if err := sender.Send(ctx, ev); err != nil {
			return Classify(err)
		}
		return nil
	}
	if !IsSystemNotificationTargetRef(d.TargetRef, d.Channel) {
		return Permanent("invalid_target", "Notification target is invalid")
	}
	var sender Notifier
	if d.Channel == contracts.NotificationChannelFeishu {
		sender = r.primary
	} else if d.Channel == contracts.NotificationChannelQQ {
		sender = r.secondary
	}
	if sender == nil {
		return Permanent("channel_unconfigured", "平台尚未配置该接收渠道")
	}
	if err := sender.Send(ctx, ev); err != nil {
		return Classify(err)
	}
	return nil
}
