package hybridrouting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

var ErrProvisionUnavailable = errors.New("hybrid routing provision is unavailable")

type ProvisionStore interface {
	GetInstance(context.Context, string) (contracts.Instance, error)
	GetHybridGatewayBinding(context.Context, int64, string, contracts.ResourceClass) (contracts.HybridGatewayBinding, error)
	UpsertHybridGatewayBinding(context.Context, contracts.HybridGatewayBinding, int64) (contracts.HybridGatewayBinding, error)
	GetVirtualKey(context.Context, int64, string) (contracts.VirtualKey, error)
}

type SecretBindingInstaller interface {
	InstallSupplyBinding(context.Context, string, string, string, string, int64, string) (contracts.ConnectorGatewayBindingInstallResult, error)
}

type AccountProvisioner interface {
	ProvisionAccount(context.Context, string, contracts.GatewayAccountSpec, string) (contracts.GatewayProvisionResult, error)
	UpdateAccount(context.Context, string, contracts.GatewayAccountSpec, string) (contracts.GatewayProvisionResult, error)
}

// Provisioner turns a pending non-secret binding into one ready NewAPI
// aggregate account. It installs the virtual key before provisioning the
// account and persists each lifecycle boundary, so retries are idempotent and
// operators can distinguish installation from remote-account failures.
type Provisioner struct {
	store     ProvisionStore
	installer SecretBindingInstaller
	accounts  AccountProvisioner
	baseURL   string
	now       func() time.Time
}

func NewProvisioner(st ProvisionStore, installer SecretBindingInstaller, accounts AccountProvisioner, supplyGatewayBaseURL string) (*Provisioner, error) {
	baseURL := strings.TrimSpace(supplyGatewayBaseURL)
	if st == nil || installer == nil || accounts == nil || baseURL == "" {
		return nil, ErrProvisionUnavailable
	}
	return &Provisioner{store: st, installer: installer, accounts: accounts, baseURL: baseURL, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (p *Provisioner) Apply(ctx context.Context, userID int64, instanceID string, class contracts.ResourceClass) (contracts.HybridGatewayBinding, error) {
	instanceID = strings.TrimSpace(instanceID)
	if p == nil || userID <= 0 || instanceID == "" || !class.IsPlatformSupply() {
		return contracts.HybridGatewayBinding{}, ErrProvisionUnavailable
	}
	instance, err := p.store.GetInstance(ctx, instanceID)
	if err != nil || instance.UserID != userID || instance.Kind != contracts.InstanceKindNewAPI || strings.TrimSpace(instance.ConnectorID) == "" {
		return contracts.HybridGatewayBinding{}, ErrProvisionUnavailable
	}
	binding, err := p.store.GetHybridGatewayBinding(ctx, userID, instanceID, class)
	if err != nil || binding.ConnectorID != instance.ConnectorID || !contracts.ValidHybridGatewayBinding(binding) {
		return contracts.HybridGatewayBinding{}, ErrProvisionUnavailable
	}
	key, err := p.store.GetVirtualKey(ctx, userID, binding.VirtualKeyID)
	if err != nil || !key.Enabled || key.InstanceID != instanceID || key.ResourceClass != class || key.KeyVersion != binding.VirtualKeyVersion ||
		key.ExpiresAt != nil && !p.now().Before(*key.ExpiresAt) {
		return p.recordError(ctx, binding, "virtual_key_unavailable", ErrProvisionUnavailable)
	}
	if binding.Status != contracts.HybridGatewayBindingInstalling {
		binding.Status, binding.ErrorCode = contracts.HybridGatewayBindingInstalling, ""
		binding, err = p.store.UpsertHybridGatewayBinding(ctx, binding, binding.Version)
		if err != nil {
			return contracts.HybridGatewayBinding{}, err
		}
	}
	installed, err := p.installer.InstallSupplyBinding(ctx, instance.ID, binding.CredentialBindingID, key.ID, key.SecretRef, key.KeyVersion, p.baseURL)
	if err != nil || installed.BindingID != binding.CredentialBindingID || installed.ChannelID != key.ID || installed.KeyVersion != key.KeyVersion {
		return p.recordError(ctx, binding, "binding_install_failed", err)
	}
	spec := contracts.GatewayAccountSpec{
		Ownership: contracts.GatewayAccountPlatformManaged, ChannelID: key.ID,
		RemoteID: binding.RemoteAccountID, DisplayName: "E2M " + string(class),
		Provider: "e2m", Type: "type_1", Models: append([]string(nil), key.Models...),
		Groups: []string{"default"}, Priority: 0, Weight: 0, Schedulable: false,
		CredentialBindingID: binding.CredentialBindingID,
	}
	var provisioned contracts.GatewayProvisionResult
	if binding.RemoteAccountID == "" {
		provisioned, err = p.accounts.ProvisionAccount(ctx, instance.ID, spec, "hybrid-supply:"+string(class))
	} else {
		provisioned, err = p.accounts.UpdateAccount(ctx, instance.ID, spec, "hybrid-supply:"+string(class))
	}
	if err != nil || strings.TrimSpace(provisioned.RemoteID) == "" {
		return p.recordError(ctx, binding, "account_provision_failed", err)
	}
	binding.RemoteAccountID = strings.TrimSpace(provisioned.RemoteID)
	binding.Status, binding.ErrorCode = contracts.HybridGatewayBindingReady, ""
	return p.store.UpsertHybridGatewayBinding(ctx, binding, binding.Version)
}

func (p *Provisioner) recordError(ctx context.Context, binding contracts.HybridGatewayBinding, code string, cause error) (contracts.HybridGatewayBinding, error) {
	binding.Status, binding.ErrorCode = contracts.HybridGatewayBindingError, code
	saved, saveErr := p.store.UpsertHybridGatewayBinding(ctx, binding, binding.Version)
	if saveErr != nil {
		return contracts.HybridGatewayBinding{}, saveErr
	}
	if cause == nil {
		cause = ErrProvisionUnavailable
	}
	return saved, fmt.Errorf("%s: %w", code, cause)
}

var _ ProvisionStore = store.Store(nil)
