package contracts

import "time"

const (
	DefaultMonitorCheckIntervalSeconds = 60
	DefaultMonitorFailStreak           = 2
	DefaultMonitorCooldownSeconds      = 300
)

// InstanceMonitorPolicy is the user-facing, per-instance monitoring policy.
// Connector transport scheduling and task lease internals are deliberately not
// part of this object.
type InstanceMonitorPolicy struct {
	InstanceID           string     `json:"instance_id"`
	UserID               int64      `json:"-"`
	Enabled              bool       `json:"enabled"`
	CheckIntervalSeconds int        `json:"check_interval_seconds"`
	FailStreak           int        `json:"fail_streak"`
	AutoSwitch           bool       `json:"auto_switch"`
	CooldownSeconds      int        `json:"cooldown_seconds"`
	DriftDetection       bool       `json:"drift_detection"`
	UpdatedAt            *time.Time `json:"updated_at,omitempty"`
}

func DefaultInstanceMonitorPolicy(instanceID string, userID int64) InstanceMonitorPolicy {
	return InstanceMonitorPolicy{
		InstanceID:           instanceID,
		UserID:               userID,
		Enabled:              true,
		CheckIntervalSeconds: DefaultMonitorCheckIntervalSeconds,
		FailStreak:           DefaultMonitorFailStreak,
		AutoSwitch:           false,
		CooldownSeconds:      DefaultMonitorCooldownSeconds,
		DriftDetection:       true,
	}
}

// AccountHealth is a health-checker verdict for one upstream account.
type AccountHealth struct {
	AccountID   string `json:"account_id"`
	DisplayName string `json:"display_name,omitempty"`
	Status      string `json:"status,omitempty"`
	Schedulable bool   `json:"schedulable"`
	Healthy     bool   `json:"healthy"`
	// FailStreak is the number of consecutive checks this account looked bad.
	FailStreak int `json:"fail_streak"`
}

// InstanceHealthSnapshot is the latest health-check result for one instance.
type InstanceHealthSnapshot struct {
	InstanceID     string          `json:"instance_id"`
	InstanceName   string          `json:"instance_name,omitempty"`
	UserID         int64           `json:"user_id,omitempty"`
	CheckedAt      time.Time       `json:"checked_at"`
	TotalAccounts  int             `json:"total_accounts"`
	HealthyCount   int             `json:"healthy_count"`
	Schedulable    int             `json:"schedulable_count"`
	Accounts       []AccountHealth `json:"accounts"`
	LastError      string          `json:"last_error,omitempty"`
	AutoSwitchNote string          `json:"auto_switch_note,omitempty"`
}
