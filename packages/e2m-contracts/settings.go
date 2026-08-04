package contracts

import "time"

// CommerceSettings is the operator-editable commerce runtime configuration.
// It lives in the unified system_settings store: environment variables only
// seed the first boot, after which the database value is authoritative and
// changes apply without a restart. Decimal strings keep exact values; an
// empty string disables the corresponding feature.
type CommerceSettings struct {
	// USDToCNYRate converts USD base-table prices into CNY sell prices.
	// Empty disables base-price-table pricing entirely (fail closed).
	USDToCNYRate string `json:"usd_to_cny_rate"`
	// BalanceAlertThreshold is the platform wallet low-balance alert line in
	// yuan. Empty disables the alert sweep.
	BalanceAlertThreshold string    `json:"balance_alert_threshold"`
	UpdatedAt             time.Time `json:"updated_at,omitempty"`
}

// UpdateCommerceSettingsRequest replaces the commerce settings section.
type UpdateCommerceSettingsRequest struct {
	USDToCNYRate          string `json:"usd_to_cny_rate"`
	BalanceAlertThreshold string `json:"balance_alert_threshold"`
}
