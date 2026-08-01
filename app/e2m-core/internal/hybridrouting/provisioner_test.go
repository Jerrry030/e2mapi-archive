package hybridrouting

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

type provisionInstallFake struct {
	instanceID, bindingID, channelID, secretRef, baseURL string
	version                                              int64
}

func (f *provisionInstallFake) InstallSupplyBinding(_ context.Context, instanceID, bindingID, channelID, secretRef string, version int64, baseURL string) (contracts.ConnectorGatewayBindingInstallResult, error) {
	f.instanceID, f.bindingID, f.channelID, f.secretRef, f.version, f.baseURL = instanceID, bindingID, channelID, secretRef, version, baseURL
	return contracts.ConnectorGatewayBindingInstallResult{BindingID: bindingID, ChannelID: channelID, KeyVersion: version}, nil
}

type provisionAccountFake struct {
	spec    contracts.GatewayAccountSpec
	creates int
	updates int
}

func (f *provisionAccountFake) ProvisionAccount(_ context.Context, _ string, spec contracts.GatewayAccountSpec, _ string) (contracts.GatewayProvisionResult, error) {
	f.creates++
	f.spec = spec
	return contracts.GatewayProvisionResult{RemoteID: "remote-economy", Created: true}, nil
}

func (f *provisionAccountFake) UpdateAccount(_ context.Context, _ string, spec contracts.GatewayAccountSpec, _ string) (contracts.GatewayProvisionResult, error) {
	f.updates++
	f.spec = spec
	return contracts.GatewayProvisionResult{RemoteID: spec.RemoteID}, nil
}

func TestProvisionerInstallsVirtualKeyBeforeReadyAccount(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	user, err := st.CreateUser(ctx, contracts.User{Email: "hybrid-provision@example.com", PasswordHash: "hash", Roles: []contracts.UserRole{contracts.UserRoleOwner}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "NewAPI", Kind: contracts.InstanceKindNewAPI, ConnectorID: "connector-hybrid"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.CreateVirtualKey(ctx, contracts.VirtualKey{UserID: user.ID, InstanceID: instance.ID, Name: "economy", ResourceClass: contracts.ResourceClassEconomy, TokenHash: contracts.HashVirtualKey("e2m_v1_test"), SecretRef: "credential_ref:hybrid/test", Models: []string{"gpt-test"}})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := st.UpsertHybridGatewayBinding(ctx, contracts.HybridGatewayBinding{
		UserID: user.ID, InstanceID: instance.ID, ResourceClass: contracts.ResourceClassEconomy, ConnectorID: instance.ConnectorID,
		CredentialBindingID: "hybrid-economy", VirtualKeyID: key.ID, VirtualKeyVersion: key.KeyVersion, Status: contracts.HybridGatewayBindingPending,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	installer, accounts := &provisionInstallFake{}, &provisionAccountFake{}
	provisioner, err := NewProvisioner(st, installer, accounts, "https://supply.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := provisioner.Apply(ctx, user.ID, instance.ID, contracts.ResourceClassEconomy)
	if err != nil || ready.Status != contracts.HybridGatewayBindingReady || ready.RemoteAccountID != "remote-economy" || ready.Version != binding.Version+2 {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	if installer.bindingID != binding.CredentialBindingID || installer.channelID != key.ID || installer.secretRef != key.SecretRef || installer.version != key.KeyVersion ||
		installer.baseURL != "https://supply.example.com/v1" {
		t.Fatalf("install=%+v", installer)
	}
	if accounts.creates != 1 || accounts.updates != 0 || accounts.spec.CredentialBindingID != binding.CredentialBindingID ||
		accounts.spec.Ownership != contracts.GatewayAccountPlatformManaged || len(accounts.spec.Models) != 1 || accounts.spec.Models[0] != "gpt-test" ||
		accounts.spec.Schedulable || accounts.spec.Weight != 0 || accounts.spec.Priority != 0 || len(accounts.spec.Groups) != 1 || accounts.spec.Groups[0] != "default" {
		t.Fatalf("account=%+v", accounts)
	}
	second, err := provisioner.Apply(ctx, user.ID, instance.ID, contracts.ResourceClassEconomy)
	if err != nil || second.Status != contracts.HybridGatewayBindingReady || accounts.creates != 1 || accounts.updates != 1 {
		t.Fatalf("retry=%+v err=%v accounts=%+v", second, err, accounts)
	}
}
