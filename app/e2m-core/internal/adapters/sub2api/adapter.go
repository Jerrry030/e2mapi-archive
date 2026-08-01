// Package sub2api provides the Core constructor for the typed connector-backed
// sub2api adapter. Native HTTP mapping lives in e2m-agent.
package sub2api

import (
	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
)

type Adapter = adapters.ConnectorGatewayClient

func New(store adapters.ConnectorTaskStore) *Adapter {
	return adapters.NewConnectorGatewayClient(store, contracts.InstanceKindSub2API)
}
