package contracts

import "time"

// PaymentProviderKey identifies a supported collection provider integration.
type PaymentProviderKey string

const (
	PaymentProviderEasyPay   PaymentProviderKey = "easypay"
	PaymentProviderAlipay    PaymentProviderKey = "alipay"
	PaymentProviderWxPay     PaymentProviderKey = "wxpay"
	PaymentProviderStripe    PaymentProviderKey = "stripe"
	PaymentProviderAirwallex PaymentProviderKey = "airwallex"
)

// PaymentProvider is the platform-admin view of one collection provider
// instance. Config contains non-sensitive values only. SecretConfigured reports
// whether a Vault reference is configured without ever returning plaintext.
type PaymentProvider struct {
	ID               string                        `json:"id"`
	ProviderKey      PaymentProviderKey            `json:"provider_key"`
	Name             string                        `json:"name"`
	Config           map[string]string             `json:"config"`
	SecretConfigured map[string]bool               `json:"secret_configured"`
	SupportedTypes   []string                      `json:"supported_types"`
	Enabled          bool                          `json:"enabled"`
	PaymentMode      string                        `json:"payment_mode,omitempty"`
	SortOrder        int                           `json:"sort_order"`
	Limits           map[string]PaymentMethodLimit `json:"limits"`
	RefundEnabled    bool                          `json:"refund_enabled"`
	AllowUserRefund  bool                          `json:"allow_user_refund"`
	CreatedAt        time.Time                     `json:"created_at"`
	UpdatedAt        time.Time                     `json:"updated_at"`
	SecretRefs       map[string]string             `json:"-"`
}

// PaymentMethodLimit configures optional per-method collection limits. Zero
// means no additional limit for that field.
type PaymentMethodLimit struct {
	SingleMin  float64 `json:"singleMin,omitempty"`
	SingleMax  float64 `json:"singleMax,omitempty"`
	DailyLimit float64 `json:"dailyLimit,omitempty"`
}

// CreatePaymentProviderRequest creates one provider instance. Secret values are
// accepted only on writes and are immediately moved behind the Vault boundary.
type CreatePaymentProviderRequest struct {
	ProviderKey     PaymentProviderKey            `json:"provider_key"`
	Name            string                        `json:"name"`
	Config          map[string]string             `json:"config"`
	Secrets         map[string]string             `json:"secrets,omitempty"`
	SupportedTypes  []string                      `json:"supported_types"`
	Enabled         bool                          `json:"enabled"`
	PaymentMode     string                        `json:"payment_mode,omitempty"`
	SortOrder       int                           `json:"sort_order"`
	Limits          map[string]PaymentMethodLimit `json:"limits,omitempty"`
	RefundEnabled   bool                          `json:"refund_enabled"`
	AllowUserRefund bool                          `json:"allow_user_refund"`
}

// UpdatePaymentProviderRequest uses patch semantics. Empty secret values keep
// existing secrets; ClearSecrets explicitly removes named secret fields.
type UpdatePaymentProviderRequest struct {
	Name            *string                        `json:"name,omitempty"`
	Config          *map[string]string             `json:"config,omitempty"`
	Secrets         map[string]string              `json:"secrets,omitempty"`
	ClearSecrets    []string                       `json:"clear_secrets,omitempty"`
	SupportedTypes  *[]string                      `json:"supported_types,omitempty"`
	Enabled         *bool                          `json:"enabled,omitempty"`
	PaymentMode     *string                        `json:"payment_mode,omitempty"`
	SortOrder       *int                           `json:"sort_order,omitempty"`
	Limits          *map[string]PaymentMethodLimit `json:"limits,omitempty"`
	RefundEnabled   *bool                          `json:"refund_enabled,omitempty"`
	AllowUserRefund *bool                          `json:"allow_user_refund,omitempty"`
}

// PaymentConfig controls the collection surface shared by all providers.
// Amounts are expressed in the configured settlement currency.
type PaymentConfig struct {
	Enabled                    bool      `json:"enabled"`
	MinAmount                  float64   `json:"min_amount"`
	MaxAmount                  float64   `json:"max_amount"`
	DailyLimit                 float64   `json:"daily_limit"`
	OrderTimeoutMinutes        int       `json:"order_timeout_minutes"`
	MaxPendingOrders           int       `json:"max_pending_orders"`
	EnabledPaymentTypes        []string  `json:"enabled_payment_types"`
	LoadBalanceStrategy        string    `json:"load_balance_strategy"`
	ProductNamePrefix          string    `json:"product_name_prefix"`
	ProductNameSuffix          string    `json:"product_name_suffix"`
	HelpImageURL               string    `json:"help_image_url"`
	HelpText                   string    `json:"help_text"`
	VisibleMethodAlipaySource  string    `json:"visible_method_alipay_source"`
	VisibleMethodWxPaySource   string    `json:"visible_method_wxpay_source"`
	VisibleMethodAlipayEnabled bool      `json:"visible_method_alipay_enabled"`
	VisibleMethodWxPayEnabled  bool      `json:"visible_method_wxpay_enabled"`
	UpdatedAt                  time.Time `json:"updated_at,omitempty"`
}

// UpdatePaymentConfigRequest updates the global collection configuration.
type UpdatePaymentConfigRequest struct {
	Enabled                    bool     `json:"enabled"`
	MinAmount                  float64  `json:"min_amount"`
	MaxAmount                  float64  `json:"max_amount"`
	DailyLimit                 float64  `json:"daily_limit"`
	OrderTimeoutMinutes        int      `json:"order_timeout_minutes"`
	MaxPendingOrders           int      `json:"max_pending_orders"`
	EnabledPaymentTypes        []string `json:"enabled_payment_types"`
	LoadBalanceStrategy        string   `json:"load_balance_strategy"`
	ProductNamePrefix          string   `json:"product_name_prefix"`
	ProductNameSuffix          string   `json:"product_name_suffix"`
	HelpImageURL               string   `json:"help_image_url"`
	HelpText                   string   `json:"help_text"`
	VisibleMethodAlipaySource  string   `json:"visible_method_alipay_source"`
	VisibleMethodWxPaySource   string   `json:"visible_method_wxpay_source"`
	VisibleMethodAlipayEnabled bool     `json:"visible_method_alipay_enabled"`
	VisibleMethodWxPayEnabled  bool     `json:"visible_method_wxpay_enabled"`
}

// PaymentOrderStatus is the durable local lifecycle state of a collection
// order. Execution states are defined for forward-compatible records even
// though payment execution, fulfillment, retry, and refund are not exposed yet.
type PaymentOrderStatus string

const (
	PaymentOrderPending           PaymentOrderStatus = "PENDING"
	PaymentOrderPaid              PaymentOrderStatus = "PAID"
	PaymentOrderRecharging        PaymentOrderStatus = "RECHARGING"
	PaymentOrderCompleted         PaymentOrderStatus = "COMPLETED"
	PaymentOrderExpired           PaymentOrderStatus = "EXPIRED"
	PaymentOrderCancelled         PaymentOrderStatus = "CANCELLED"
	PaymentOrderFailed            PaymentOrderStatus = "FAILED"
	PaymentOrderRefundRequested   PaymentOrderStatus = "REFUND_REQUESTED"
	PaymentOrderRefunding         PaymentOrderStatus = "REFUNDING"
	PaymentOrderRefundPending     PaymentOrderStatus = "REFUND_PENDING"
	PaymentOrderPartiallyRefunded PaymentOrderStatus = "PARTIALLY_REFUNDED"
	PaymentOrderRefunded          PaymentOrderStatus = "REFUNDED"
	PaymentOrderRefundFailed      PaymentOrderStatus = "REFUND_FAILED"
)

type PaymentOrderType string

const (
	PaymentOrderBalance      PaymentOrderType = "balance"
	PaymentOrderSubscription PaymentOrderType = "subscription"
)

// PaymentOrder is the administrator-visible, non-secret local order record.
// Monetary values are decimal strings to avoid JSON floating-point drift.
// Provider configuration snapshots and secret references are never exposed.
type PaymentOrder struct {
	ID                  string             `json:"id"`
	UserID              int64              `json:"user_id"`
	UserEmail           string             `json:"user_email,omitempty"`
	UserName            string             `json:"user_name,omitempty"`
	Amount              string             `json:"amount"`
	PayAmount           string             `json:"pay_amount"`
	FeeRate             string             `json:"fee_rate"`
	Currency            string             `json:"currency"`
	PaymentType         string             `json:"payment_type"`
	OutTradeNo          string             `json:"out_trade_no"`
	PaymentTradeNo      string             `json:"payment_trade_no,omitempty"`
	ProviderOrderID     string             `json:"provider_order_id,omitempty"`
	OrderType           PaymentOrderType   `json:"order_type"`
	ProviderInstanceID  string             `json:"provider_instance_id,omitempty"`
	ProviderKey         PaymentProviderKey `json:"provider_key,omitempty"`
	ProviderName        string             `json:"provider_name,omitempty"`
	Status              PaymentOrderStatus `json:"status"`
	RefundAmount        string             `json:"refund_amount"`
	RefundReason        string             `json:"refund_reason,omitempty"`
	RefundRequestedAt   *time.Time         `json:"refund_requested_at,omitempty"`
	RefundRequestedBy   string             `json:"refund_requested_by,omitempty"`
	RefundRequestReason string             `json:"refund_request_reason,omitempty"`
	ExpiresAt           time.Time          `json:"expires_at"`
	PaidAt              *time.Time         `json:"paid_at,omitempty"`
	CompletedAt         *time.Time         `json:"completed_at,omitempty"`
	FailedAt            *time.Time         `json:"failed_at,omitempty"`
	FailedReason        string             `json:"failed_reason,omitempty"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

// PaymentOrderFilter is the persistence query shape. StartCreatedAt is
// inclusive and EndCreatedAt is exclusive.
type PaymentOrderFilter struct {
	Page               int
	PageSize           int
	Status             PaymentOrderStatus
	PaymentType        string
	OrderType          PaymentOrderType
	UserID             int64
	ProviderInstanceID string
	Keyword            string
	StartCreatedAt     *time.Time
	EndCreatedAt       *time.Time
}

type PaymentOrderPage struct {
	Items    []PaymentOrder `json:"items"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}

type PaymentOrderDetail struct {
	Order     PaymentOrder     `json:"order"`
	AuditLogs []OperationAudit `json:"audit_logs"`
}
