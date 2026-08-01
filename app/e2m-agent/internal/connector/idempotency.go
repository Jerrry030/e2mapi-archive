package connector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"e2m.local/contracts"
)

// taskSignature covers the whole leased task identity and is used by durable
// non-write outboxes. Gateway mutation receipts intentionally use the logical
// idempotency key plus writeTaskPayloadSignature instead.
func taskSignature(task contracts.ConnectorTask) string {
	return hex.EncodeToString(taskSignatureBytes(task))
}

func taskSignatureBytes(task contracts.ConnectorTask) []byte {
	value := struct {
		ConnectorID    string                      `json:"connector_id"`
		InstanceID     string                      `json:"instance_id"`
		Type           contracts.ConnectorTaskType `json:"type"`
		SchemaVersion  int                         `json:"schema_version"`
		Input          json.RawMessage             `json:"input"`
		IdempotencyKey string                      `json:"idempotency_key"`
	}{task.ConnectorID, task.InstanceID, task.Type, task.SchemaVersion, task.Input, task.IdempotencyKey}
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return sum[:]
}
