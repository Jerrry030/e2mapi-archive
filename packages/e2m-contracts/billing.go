package contracts

import "time"

// BillingLine is one charge line on a statement.
type BillingLine struct {
	Item      string `json:"item"`
	Quantity  int64  `json:"quantity"`
	UnitPrice string `json:"unit_price"`
	Amount    string `json:"amount"`
	Note      string `json:"note,omitempty"`
}

// BillingStatement is the per-user, per-period hosting bill. Billing follows
// the side-car trust model: fixed hosting fee per managed instance plus a fee
// per automated/manual disposition. Gateway-reported usage is reference-only
// (the owner controls the gateway, so it is not a trustworthy billing basis).
type BillingStatement struct {
	UserID           int64         `json:"user_id"`
	UserEmail        string        `json:"user_email,omitempty"`
	Period           string        `json:"period"` // YYYY-MM
	PeriodStart      time.Time     `json:"period_start"`
	PeriodEnd        time.Time     `json:"period_end"`
	InstanceCount    int64         `json:"instance_count"`
	DispositionCount int64         `json:"disposition_count"`
	Lines            []BillingLine `json:"lines"`
	Total            string        `json:"total"`
	Currency         string        `json:"currency"`
	GeneratedAt      time.Time     `json:"generated_at"`
}
