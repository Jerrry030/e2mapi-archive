package keyproof

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
)

type proofFunc func(context.Context, string, contracts.ConnectorGatewayBindingProofInput) (contracts.ConnectorGatewayBindingProofResult, error)

func (f proofFunc) ProveBinding(ctx context.Context, instanceID string, input contracts.ConnectorGatewayBindingProofInput) (contracts.ConnectorGatewayBindingProofResult, error) {
	return f(ctx, instanceID, input)
}

type proofStore struct {
	store.Store
	instance  contracts.Instance
	connector contracts.Connector
}

type installProofBridge struct {
	proofFunc
	input  contracts.ConnectorGatewayBindingInstallInput
	result contracts.ConnectorGatewayBindingInstallResult
}

func (b *installProofBridge) InstallBinding(_ context.Context, _ string, input contracts.ConnectorGatewayBindingInstallInput) (contracts.ConnectorGatewayBindingInstallResult, error) {
	b.input = input
	return b.result, nil
}

func (s proofStore) GetConnector(ctx context.Context, id string) (contracts.Connector, error) {
	if id == s.connector.ID {
		return s.connector, nil
	}
	return s.Store.GetConnector(ctx, id)
}

func (s proofStore) GetInstance(ctx context.Context, id string) (contracts.Instance, error) {
	if id == s.instance.ID {
		return s.instance, nil
	}
	return s.Store.GetInstance(ctx, id)
}

func seedProofService(t *testing.T, connectorKey string) (*Service, store.Store, contracts.UpstreamChannel, string) {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemoryStore(time.Now().UTC())
	v := vault.NewMemoryVault()
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: 7, Name: "gateway", Kind: contracts.InstanceKindNewAPI})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "pool"})
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "source-a", DisplayName: "channel",
		AccountOwnership: contracts.GatewayAccountPlatformManaged, CredentialBindingID: "binding-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: instance.UserID, InstanceID: instance.ID, PoolID: pool.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimPlanChannels(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	ref := "credential_ref:test"
	_, _ = v.Store(ctx, ref, "sk-core-key")
	if _, err := st.UpsertUpstreamKeyDelivery(ctx, contracts.UpstreamKeyDelivery{ChannelID: channel.ID, SecretRef: ref, MaskedValue: "********-key"}); err != nil {
		t.Fatal(err)
	}
	prover := proofFunc(func(_ context.Context, gotInstance string, input contracts.ConnectorGatewayBindingProofInput) (contracts.ConnectorGatewayBindingProofResult, error) {
		if gotInstance != instance.ID {
			t.Fatalf("instance = %q", gotInstance)
		}
		mac := hmac.New(sha256.New, []byte(connectorKey))
		_, _ = mac.Write(contracts.ConnectorGatewayBindingProofMessage(input))
		return contracts.ConnectorGatewayBindingProofResult{Proof: hex.EncodeToString(mac.Sum(nil))}, nil
	})
	instance.ConnectorID = "connector-a"
	lastSeen := time.Now().UTC()
	wrapped := &proofStore{Store: st, instance: instance, connector: contracts.Connector{
		ID: "connector-a", InstanceID: instance.ID, UserID: instance.UserID,
		Status: contracts.ConnectorStatusOnline, LastSeenAt: &lastSeen,
	}}
	return New(wrapped, v, prover), st, channel, instance.ID
}

func TestVerifyPersistsChallengeBoundMatch(t *testing.T) {
	service, st, channel, instanceID := seedProofService(t, "sk-core-key")
	verification, err := service.Verify(context.Background(), channel.ID, instanceID)
	if err != nil {
		t.Fatal(err)
	}
	if !verification.FreshlyVerified || !verification.DeploymentRequired ||
		verification.Proof.Status != contracts.DeliveryKeyProofVerified || verification.Proof.ConnectorID != "connector-a" ||
		verification.Proof.InstanceID != instanceID || verification.Proof.CheckedAt.IsZero() {
		t.Fatalf("verification = %+v", verification)
	}
	persisted, _ := st.GetUpstreamKeyDelivery(context.Background(), channel.ID)
	if persisted.ProofStatus != contracts.DeliveryKeyProofVerified {
		t.Fatalf("persisted = %+v", persisted)
	}
	receipt, err := st.GetUpstreamKeyProofReceipt(context.Background(), channel.ID, instanceID)
	if err != nil || receipt.Status != contracts.DeliveryKeyProofVerified || receipt.ConnectorID != "connector-a" {
		t.Fatalf("receipt = %+v, %v", receipt, err)
	}
	if _, err := st.UpsertUpstreamKeyDeployment(context.Background(), contracts.UpstreamKeyDeployment{
		ChannelID: channel.ID, InstanceID: instanceID, KeyVersion: persisted.KeyVersion,
		ConnectorID: "connector-a", Status: contracts.DeliveryKeyDeploymentDeployed,
	}); err != nil {
		t.Fatal(err)
	}
	verification, err = service.Verify(context.Background(), channel.ID, instanceID)
	if err != nil || verification.DeploymentRequired {
		t.Fatalf("deployed verification = %+v, %v", verification, err)
	}
}

func TestVerifyRejectsStaleConnectorBeforeQueuingProof(t *testing.T) {
	service, _, channel, instanceID := seedProofService(t, "sk-core-key")
	wrapped := service.store.(*proofStore)
	stale := time.Now().UTC().Add(-2 * time.Minute)
	wrapped.connector.LastSeenAt = &stale
	if _, err := service.Verify(context.Background(), channel.ID, instanceID); !errors.Is(err, ErrUnverified) {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyMismatchFailsClosedAndPersistsEvidence(t *testing.T) {
	service, st, channel, instanceID := seedProofService(t, "sk-different")
	_, err := service.Verify(context.Background(), channel.ID, instanceID)
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("err = %v", err)
	}
	persisted, _ := st.GetUpstreamKeyDelivery(context.Background(), channel.ID)
	if persisted.ProofStatus != contracts.DeliveryKeyProofMismatch {
		t.Fatalf("persisted = %+v", persisted)
	}
	receipt, receiptErr := st.GetUpstreamKeyProofReceipt(context.Background(), channel.ID, instanceID)
	if receiptErr != nil || receipt.Status != contracts.DeliveryKeyProofMismatch {
		t.Fatalf("receipt = %+v, %v", receipt, receiptErr)
	}
}

func TestVerifyTransportFailureClearsStaleVerifiedState(t *testing.T) {
	service, st, channel, instanceID := seedProofService(t, "sk-core-key")
	if _, err := service.Verify(context.Background(), channel.ID, instanceID); err != nil {
		t.Fatal(err)
	}
	service.prover = proofFunc(func(context.Context, string, contracts.ConnectorGatewayBindingProofInput) (contracts.ConnectorGatewayBindingProofResult, error) {
		return contracts.ConnectorGatewayBindingProofResult{}, errors.New("offline")
	})
	if _, err := service.Verify(context.Background(), channel.ID, instanceID); !errors.Is(err, ErrUnverified) {
		t.Fatalf("err = %v", err)
	}
	persisted, _ := st.GetUpstreamKeyDelivery(context.Background(), channel.ID)
	if persisted.ProofStatus != contracts.DeliveryKeyProofUnverified {
		t.Fatalf("persisted = %+v", persisted)
	}
	receipt, receiptErr := st.GetUpstreamKeyProofReceipt(context.Background(), channel.ID, instanceID)
	if receiptErr != nil || receipt.Status != contracts.DeliveryKeyProofUnverified {
		t.Fatalf("receipt = %+v, %v", receipt, receiptErr)
	}
}

func TestVerifyRejectsCrossOwnerConnectorIdentity(t *testing.T) {
	service, _, channel, instanceID := seedProofService(t, "sk-core-key")
	wrapper := service.store.(*proofStore)
	wrapper.connector.UserID++
	if _, err := service.Verify(context.Background(), channel.ID, instanceID); !errors.Is(err, ErrUnverified) {
		t.Fatalf("cross-owner connector error=%v", err)
	}
}

func TestVerifyRejectsOversizeSecret(t *testing.T) {
	service, _, channel, instanceID := seedProofService(t, "sk-core-key")
	delivery, err := service.store.GetUpstreamKeyDelivery(context.Background(), channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.vault.Store(context.Background(), delivery.SecretRef, strings.Repeat("x", contracts.ConnectorBindingEncryptionMaxPlaintext+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Verify(context.Background(), channel.ID, instanceID); !errors.Is(err, ErrUnverified) {
		t.Fatalf("oversize secret error=%v", err)
	}
}

func TestVerifyProofReceiptsDoNotOverwriteAnotherInstance(t *testing.T) {
	service, st, channel, firstInstanceID := seedProofService(t, "sk-core-key")
	if _, err := service.Verify(context.Background(), channel.ID, firstInstanceID); err != nil {
		t.Fatal(err)
	}
	secondInstance, err := st.CreateInstance(context.Background(), contracts.Instance{
		UserID: 7, Name: "gateway-b", Kind: contracts.InstanceKindNewAPI,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondInstance.ConnectorID = "connector-b"
	lastSeen := time.Now().UTC()
	wrapped := service.store.(*proofStore)
	secondStore := &proofStore{Store: st, instance: secondInstance, connector: contracts.Connector{
		ID: "connector-b", InstanceID: secondInstance.ID, UserID: secondInstance.UserID,
		Status: contracts.ConnectorStatusOnline, LastSeenAt: &lastSeen,
	}}
	service.store = secondStore
	service.prover = proofFunc(func(_ context.Context, gotInstance string, input contracts.ConnectorGatewayBindingProofInput) (contracts.ConnectorGatewayBindingProofResult, error) {
		if gotInstance != secondInstance.ID {
			t.Fatalf("instance = %q", gotInstance)
		}
		mac := hmac.New(sha256.New, []byte("sk-wrong"))
		_, _ = mac.Write(contracts.ConnectorGatewayBindingProofMessage(input))
		return contracts.ConnectorGatewayBindingProofResult{Proof: hex.EncodeToString(mac.Sum(nil))}, nil
	})
	if _, err := service.Verify(context.Background(), channel.ID, secondInstance.ID); !errors.Is(err, ErrMismatch) {
		t.Fatalf("second proof error = %v", err)
	}
	receiptA, errA := wrapped.Store.GetUpstreamKeyProofReceipt(context.Background(), channel.ID, firstInstanceID)
	receiptB, errB := wrapped.Store.GetUpstreamKeyProofReceipt(context.Background(), channel.ID, secondInstance.ID)
	if errA != nil || errB != nil || receiptA.Status != contracts.DeliveryKeyProofVerified || receiptA.ConnectorID != "connector-a" ||
		receiptB.Status != contracts.DeliveryKeyProofMismatch || receiptB.ConnectorID != "connector-b" {
		t.Fatalf("receipts A=%+v/%v B=%+v/%v", receiptA, errA, receiptB, errB)
	}
}

func TestInstallBindingResolvesVaultOnlyIntoAuthenticatedCiphertext(t *testing.T) {
	service, _, channel, instanceID := seedProofService(t, "sk-core-key")
	wrapped := service.store.(*proofStore)
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrapped.connector.Gateway = contracts.ConnectorRuntimeState{
		ProtocolVersion:            contracts.ConnectorProtocolVersion,
		Capabilities:               []contracts.ConnectorTaskType{contracts.ConnectorTaskGatewayBindingInstall},
		BindingEncryptionPublicKey: base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()),
	}
	bridge := &installProofBridge{proofFunc: service.prover.(proofFunc), result: contracts.ConnectorGatewayBindingInstallResult{
		BindingID: channel.CredentialBindingID, ChannelID: channel.ID, KeyVersion: 1,
	}}
	service.installer = bridge
	result, err := service.InstallBinding(context.Background(), channel.ID, instanceID)
	if err != nil || result != bridge.result {
		t.Fatalf("install result=%+v err=%v", result, err)
	}
	if bridge.input.BindingID != channel.CredentialBindingID || bridge.input.ChannelID != channel.ID || bridge.input.KeyVersion != 1 ||
		strings.Contains(bridge.input.Ciphertext, "sk-core-key") || !contracts.IsConnectorBindingCiphertext(bridge.input.Ciphertext) {
		t.Fatalf("install input = %+v", bridge.input)
	}
	plaintext := decryptBindingForTest(t, private, wrapped.connector.ID, instanceID, bridge.input)
	if plaintext != "sk-core-key" {
		t.Fatalf("decrypted binding = %q", plaintext)
	}
}

func TestInstallBindingFailsClosedWithoutAdvertisedCapability(t *testing.T) {
	service, _, channel, instanceID := seedProofService(t, "sk-core-key")
	service.installer = &installProofBridge{}
	if _, err := service.InstallBinding(context.Background(), channel.ID, instanceID); !errors.Is(err, ErrUnverified) {
		t.Fatalf("unsupported connector error = %v", err)
	}
}

func TestInstallSecretBindingEncryptsVirtualKeyIdentity(t *testing.T) {
	service, _, _, instanceID := seedProofService(t, "delivery-secret")
	wrapped := service.store.(*proofStore)
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrapped.connector.Gateway = contracts.ConnectorRuntimeState{
		ProtocolVersion: contracts.ConnectorProtocolVersion, Capabilities: []contracts.ConnectorTaskType{contracts.ConnectorTaskGatewayBindingInstall},
		BindingEncryptionPublicKey: base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()),
	}
	if _, err := service.vault.Store(context.Background(), "credential_ref:virtual/hybrid", "e2m_v1_virtual"); err != nil {
		t.Fatal(err)
	}
	bridge := &installProofBridge{proofFunc: service.prover.(proofFunc), result: contracts.ConnectorGatewayBindingInstallResult{
		BindingID: "hybrid-economy", ChannelID: "virtual-key-1", KeyVersion: 3,
	}}
	service.installer = bridge
	result, err := service.InstallSecretBinding(context.Background(), instanceID, "hybrid-economy", "virtual-key-1", "credential_ref:virtual/hybrid", 3)
	if err != nil || result != bridge.result {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if bridge.input.BindingID != "hybrid-economy" || bridge.input.ChannelID != "virtual-key-1" || bridge.input.KeyVersion != 3 ||
		strings.Contains(bridge.input.Ciphertext, "e2m_v1_virtual") || strings.Contains(bridge.input.Ciphertext, "credential_ref") {
		t.Fatalf("input=%+v", bridge.input)
	}
	if got := decryptBindingForTest(t, private, wrapped.connector.ID, instanceID, bridge.input); got != "e2m_v1_virtual" {
		t.Fatalf("plaintext=%q", got)
	}
}

func TestInstallSupplyBindingEncryptsRestrictedNewAPICredential(t *testing.T) {
	service, _, _, instanceID := seedProofService(t, "delivery-secret")
	wrapped := service.store.(*proofStore)
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrapped.connector.Gateway = contracts.ConnectorRuntimeState{
		ProtocolVersion: contracts.ConnectorProtocolVersion, Capabilities: []contracts.ConnectorTaskType{contracts.ConnectorTaskGatewayBindingInstall},
		BindingEncryptionPublicKey: base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()),
	}
	if _, err := service.vault.Store(context.Background(), "credential_ref:virtual/supply", "e2m_v1_virtual"); err != nil {
		t.Fatal(err)
	}
	bridge := &installProofBridge{proofFunc: service.prover.(proofFunc), result: contracts.ConnectorGatewayBindingInstallResult{
		BindingID: "hybrid-stable", ChannelID: "virtual-key-2", KeyVersion: 4,
	}}
	service.installer = bridge
	_, err = service.InstallSupplyBinding(context.Background(), instanceID, "hybrid-stable", "virtual-key-2", "credential_ref:virtual/supply", 4, "https://supply.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	plaintext := decryptBindingForTest(t, private, wrapped.connector.ID, instanceID, bridge.input)
	var credential struct {
		Key     string `json:"key"`
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal([]byte(plaintext), &credential); err != nil || credential.Key != "e2m_v1_virtual" || credential.BaseURL != "https://supply.example.com/v1" {
		t.Fatalf("credential=%+v plaintext=%q err=%v", credential, plaintext, err)
	}
	for _, invalid := range []string{"", "http://supply.example.com/v1", "https://user@supply.example.com/v1", "https://supply.example.com/v1?key=secret"} {
		if _, err := service.InstallSupplyBinding(context.Background(), instanceID, "hybrid-stable", "virtual-key-2", "credential_ref:virtual/supply", 4, invalid); !errors.Is(err, ErrUnverified) {
			t.Fatalf("invalid base URL %q error=%v", invalid, err)
		}
	}
}

func decryptBindingForTest(t *testing.T, private *ecdh.PrivateKey, connectorID, instanceID string, input contracts.ConnectorGatewayBindingInstallInput) string {
	t.Helper()
	var envelope bindingCiphertextEnvelope
	if err := json.Unmarshal([]byte(input.Ciphertext), &envelope); err != nil {
		t.Fatal(err)
	}
	ephemeralBytes, _ := base64.RawURLEncoding.DecodeString(envelope.EphemeralPublicKey)
	ephemeral, _ := ecdh.X25519().NewPublicKey(ephemeralBytes)
	shared, _ := private.ECDH(ephemeral)
	key, _ := hkdf.Key(sha256.New, shared, nil, contracts.ConnectorBindingEncryptionKeyDomain, 32)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce, _ := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	sealed, _ := base64.RawURLEncoding.DecodeString(envelope.Sealed)
	plaintext, err := aead.Open(nil, nonce, sealed, contracts.ConnectorGatewayBindingEncryptionAAD(connectorID, instanceID, input.BindingID, input.ChannelID, input.KeyVersion))
	if err != nil {
		t.Fatal(err)
	}
	return string(plaintext)
}
