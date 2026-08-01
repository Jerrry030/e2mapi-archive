package contracts

import "time"

// ApprovalStatus tracks an approval request through its lifecycle.
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalRejected ApprovalStatus = "rejected"
	ApprovalExecuted ApprovalStatus = "executed"
	ApprovalFailed   ApprovalStatus = "failed"
)

// ApprovalActionBatchSchedulable is the W6 L2 action: set schedulable on many
// accounts of one instance at once.
const ApprovalActionBatchSchedulable = "batch_set_schedulable"

// ApprovalRequest is one pending L2/L3 action awaiting a human decision. The
// action executes only after approval; every transition is audited with
// ApprovalID back-references.
type ApprovalRequest struct {
	ID          string         `json:"id"`
	UserID      int64          `json:"user_id"`
	InstanceID  string         `json:"instance_id"`
	Action      string         `json:"action"`
	RiskLevel   RiskLevel      `json:"risk_level"`
	AccountIDs  []string       `json:"account_ids,omitempty"`
	Schedulable *bool          `json:"schedulable,omitempty"` // for batch_set_schedulable
	Reason      string         `json:"reason,omitempty"`
	Status      ApprovalStatus `json:"status"`
	RequestedBy string         `json:"requested_by"`
	DecidedBy   string         `json:"decided_by,omitempty"`
	DecidedAt   *time.Time     `json:"decided_at,omitempty"`
	ResultNote  string         `json:"result_note,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}
