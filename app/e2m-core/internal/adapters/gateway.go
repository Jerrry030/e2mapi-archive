package adapters

import (
	"context"

	"e2m.local/contracts"
)

// GatewayAdapter is the gateway-neutral operations surface used by Core. The
// connector runtime, not Core, owns gateway-specific HTTP mappings.
type GatewayAdapter interface {
	Kind() contracts.InstanceKind
	Capabilities() []contracts.AdapterCapability
	ListAccounts(ctx context.Context, instance contracts.Instance) ([]contracts.GatewayAccount, error)
	SetSchedulable(ctx context.Context, instance contracts.Instance, accountID string, schedulable bool) error

	// ProvisionAccount creates (or adopts) a remote account/channel from a
	// platform-managed spec and returns its gateway-native id. Implementations
	// must be idempotent on the spec's stable external name (ChannelID): if an
	// account with that name already exists, adopt it instead of creating a
	// duplicate. Binding IDs in spec are opaque and must never be resolved by Core.
	ProvisionAccount(ctx context.Context, instance contracts.Instance, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error)
	// UpdateAccount pushes a changed spec onto an existing remote account
	// identified by spec.RemoteID (display name, models, groups, priority and
	// weight). Connector-local binding resolution is not implemented yet.
	UpdateAccount(ctx context.Context, instance contracts.Instance, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error)
	// DeleteAccount removes a remote account/channel the platform created.
	// Deleting an already-absent account is not an error (idempotent).
	DeleteAccount(ctx context.Context, instance contracts.Instance, accountID string) error
}

// QualityProbeAdapter is an optional read-only capability. Keeping it separate
// from GatewayAdapter lets older/in-process adapters remain scheduling-capable
// without pretending they can produce real recovery evidence.
type QualityProbeAdapter interface {
	ProbeQuality(
		ctx context.Context,
		instance contracts.Instance,
		input contracts.ConnectorGatewayQualityProbeInput,
		identity string,
	) (contracts.ConnectorGatewayQualityProbeResult, error)
}

// BindingProofAdapter proves possession of a Connector-local binding value by
// returning a random-challenge HMAC. Plaintext never crosses this interface.
type BindingProofAdapter interface {
	ProveBinding(
		ctx context.Context,
		instance contracts.Instance,
		input contracts.ConnectorGatewayBindingProofInput,
	) (contracts.ConnectorGatewayBindingProofResult, error)
}

// BindingInstaller queues an already-encrypted binding envelope. Plaintext is
// never accepted by adapters or persisted in a Connector task.
type BindingInstaller interface {
	InstallBinding(
		ctx context.Context,
		instance contracts.Instance,
		input contracts.ConnectorGatewayBindingInstallInput,
	) (contracts.ConnectorGatewayBindingInstallResult, error)
}

// SchedulingBarrierAdapter is implemented by adapters that can durably reject
// scheduling commands older than the current operation. The barrier advances
// that ordering watermark without changing gateway state.
type SchedulingBarrierAdapter interface {
	SchedulingBarrier(ctx context.Context, instance contracts.Instance) error
}

// TrafficShareAdapter is optional because only gateways with a verified native
// integer weight operation may participate in staged traffic rollout.
type TrafficShareAdapter interface {
	SetTrafficShare(ctx context.Context, instance contracts.Instance, accountID string, weight int) error
}

func gatewayCapabilities(kind contracts.InstanceKind) []contracts.AdapterCapability {
	all := contracts.ExecutableGatewayCapabilities()
	out := make([]contracts.AdapterCapability, 0, 6)
	for _, capability := range all {
		if capability.System == kind {
			out = append(out, capability)
		}
	}
	return out
}
