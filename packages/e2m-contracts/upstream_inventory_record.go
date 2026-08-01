package contracts

import "time"

// UpstreamInventoryStateRecord is returned after an explicit supply-admission
// transition. Keeping it separate avoids overloading gateway scheduling state.
type UpstreamInventoryStateRecord struct {
	ChannelID string                 `json:"channel_id"`
	State     UpstreamInventoryState `json:"state"`
	UpdatedAt time.Time              `json:"updated_at"`
}
