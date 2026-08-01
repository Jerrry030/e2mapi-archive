// Package cpa provides the Core constructor for the typed connector-backed CPA
// adapter. Native HTTP mapping lives in e2m-agent.
package cpa

import (
	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
)

type Adapter = adapters.ConnectorGatewayClient

func New(store adapters.ConnectorTaskStore) *Adapter {
	return adapters.NewConnectorGatewayClient(store, contracts.InstanceKindCPA)
}
