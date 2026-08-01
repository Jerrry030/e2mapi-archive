package contracts

import "time"

type SecretKind string

const (
	SecretKindNotification SecretKind = "notification"
	SecretKindUpstream     SecretKind = "upstream"
	SecretKindProxy        SecretKind = "proxy"
)

type SecretRef struct {
	Ref       string     `json:"ref"`
	UserID    int64      `json:"user_id"`
	Kind      SecretKind `json:"kind"`
	Name      string     `json:"name"`
	Exists    bool       `json:"exists"`
	CreatedAt time.Time  `json:"created_at,omitempty"`
	UpdatedAt time.Time  `json:"updated_at,omitempty"`
}

type UpsertSecretRequest struct {
	UserID int64      `json:"user_id"`
	Kind   SecretKind `json:"kind"`
	Name   string     `json:"name"`
	Value  string     `json:"value"`
}

type UpsertSecretResponse struct {
	Secret SecretRef `json:"secret"`
}
