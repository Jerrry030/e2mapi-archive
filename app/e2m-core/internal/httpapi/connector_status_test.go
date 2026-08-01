package httpapi

import (
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestEffectiveConnectorStatusUsesSixtySecondThreshold(t *testing.T) {
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	seen := now.Add(-59 * time.Second)
	connector := contracts.Connector{Status: contracts.ConnectorStatusOnline, LastSeenAt: &seen}
	if got := effectiveConnectorStatus(connector, now); got.Status != contracts.ConnectorStatusOnline {
		t.Fatalf("connector seen 59s ago = %s, want online", got.Status)
	}
	seen = now.Add(-61 * time.Second)
	connector.LastSeenAt = &seen
	if got := effectiveConnectorStatus(connector, now); got.Status != contracts.ConnectorStatusOffline {
		t.Fatalf("connector seen 61s ago = %s, want offline", got.Status)
	}
	connector.Status = contracts.ConnectorStatusRevoked
	if got := effectiveConnectorStatus(connector, now); got.Status != contracts.ConnectorStatusRevoked {
		t.Fatalf("revoked connector = %s, want revoked", got.Status)
	}
}
