// Package newapi provides the Core constructor for the typed connector-backed
// new-api adapter. Native HTTP mapping lives in e2m-agent.
package newapi

import (
	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
)

type Adapter = adapters.ConnectorGatewayClient

func New(store adapters.ConnectorTaskStore) *Adapter {
	return adapters.NewConnectorGatewayClient(store, contracts.InstanceKindNewAPI)
}
