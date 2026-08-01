// Package keyproof verifies that a Connector-local credential binding contains
// the same key as Core's owner-delivery secret without moving either plaintext
// value across the Connector boundary.
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
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
)

var (
	ErrUnverified = errors.New("delivery key binding proof is unverified")
	ErrMismatch   = errors.New("delivery key does not match connector binding")
)

type ConnectorProver interface {
	ProveBinding(context.Context, string, contracts.ConnectorGatewayBindingProofInput) (contracts.ConnectorGatewayBindingProofResult, error)
}

type ConnectorBindingInstaller interface {
	InstallBinding(context.Context, string, contracts.ConnectorGatewayBindingInstallInput) (contracts.ConnectorGatewayBindingInstallResult, error)
}

type Verification struct {
	Delivery           contracts.UpstreamKeyDelivery
	Proof              contracts.UpstreamKeyProofReceipt
	FreshlyVerified    bool
	DeploymentRequired bool
}

type Service struct {
	store     store.Store
	vault     vault.Vault
	prover    ConnectorProver
	installer ConnectorBindingInstaller
	random    io.Reader
	now       func() time.Time
}

func New(st store.Store, v vault.Vault, prover ConnectorProver) *Service {
	service := &Service{store: st, vault: v, prover: prover, random: rand.Reader, now: time.Now}
	if installer, ok := prover.(ConnectorBindingInstaller); ok {
		service.installer = installer
	}
	return service
}

// InstallBinding resolves one delivery key from Vault only long enough to
// encrypt it to the active Connector. The queued task receives ciphertext and
// immutable non-sensitive metadata, never the plaintext or secret ref.
func (s *Service) InstallBinding(ctx context.Context, channelID, instanceID string) (contracts.ConnectorGatewayBindingInstallResult, error) {
	channelID, instanceID = strings.TrimSpace(channelID), strings.TrimSpace(instanceID)
	if s == nil || s.store == nil || s.vault == nil || s.installer == nil || channelID == "" || instanceID == "" {
		return contracts.ConnectorGatewayBindingInstallResult{}, ErrUnverified
	}
	channel, err := s.store.GetUpstreamChannel(ctx, channelID)
	if err != nil || channel.AccountOwnership.Normalize() != contracts.GatewayAccountPlatformManaged ||
		strings.TrimSpace(channel.CredentialBindingID) == "" {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: load managed channel", ErrUnverified)
	}
	if !contracts.IsConnectorQualityProbeField(channel.ID) || !contracts.IsConnectorQualityProbeField(strings.TrimSpace(channel.CredentialBindingID)) {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: managed channel identifiers are invalid", ErrUnverified)
	}
	delivery, err := s.store.GetUpstreamKeyDelivery(ctx, channelID)
	if err != nil || delivery.KeyVersion <= 0 || strings.TrimSpace(delivery.SecretRef) == "" {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: load delivery key", ErrUnverified)
	}
	instance, err := s.store.GetInstance(ctx, instanceID)
	if err != nil || instance.UserID <= 0 {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: connector is unavailable", ErrUnverified)
	}
	assigned, err := s.store.ListAssignedUpstreamKeys(ctx, instance.UserID)
	if err != nil || !deliveryAssignedToUser(delivery, assigned) {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: delivery key is not allocated to the instance owner", ErrUnverified)
	}
	return s.InstallSecretBinding(ctx, instanceID, channel.CredentialBindingID, channel.ID, delivery.SecretRef, delivery.KeyVersion)
}

// InstallSecretBinding is the shared Vault-to-Connector delivery primitive.
// It is intentionally agnostic to whether the secret represents a legacy
// assigned delivery key or a Hybrid Supply virtual key; policy and ownership
// checks stay with the calling workflow. Plaintext exists only while building
// an envelope encrypted to the live Connector and never enters a task or DB.
func (s *Service) InstallSecretBinding(ctx context.Context, instanceID, bindingID, channelID, secretRef string, keyVersion int64) (contracts.ConnectorGatewayBindingInstallResult, error) {
	return s.installSecretBinding(ctx, instanceID, bindingID, channelID, secretRef, keyVersion, "")
}

// InstallSupplyBinding installs the virtual key together with the one explicit
// Supply Gateway base URL. The JSON object is encrypted as a whole; neither the
// key, the URL nor the Vault ref enters a Connector task or Core persistence.
func (s *Service) InstallSupplyBinding(ctx context.Context, instanceID, bindingID, channelID, secretRef string, keyVersion int64, baseURL string) (contracts.ConnectorGatewayBindingInstallResult, error) {
	baseURL = strings.TrimSpace(baseURL)
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != baseURL {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: supply gateway base URL is invalid", ErrUnverified)
	}
	return s.installSecretBinding(ctx, instanceID, bindingID, channelID, secretRef, keyVersion, baseURL)
}

func (s *Service) installSecretBinding(ctx context.Context, instanceID, bindingID, channelID, secretRef string, keyVersion int64, baseURL string) (contracts.ConnectorGatewayBindingInstallResult, error) {
	instanceID, bindingID, channelID, secretRef = strings.TrimSpace(instanceID), strings.TrimSpace(bindingID), strings.TrimSpace(channelID), strings.TrimSpace(secretRef)
	if s == nil || s.store == nil || s.vault == nil || s.installer == nil || keyVersion <= 0 || secretRef == "" ||
		!contracts.IsConnectorQualityProbeField(instanceID) || !contracts.IsConnectorQualityProbeField(bindingID) || !contracts.IsConnectorQualityProbeField(channelID) {
		return contracts.ConnectorGatewayBindingInstallResult{}, ErrUnverified
	}
	instance, err := s.store.GetInstance(ctx, instanceID)
	if err != nil || strings.TrimSpace(instance.ConnectorID) == "" || instance.UserID == 0 {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: connector is unavailable", ErrUnverified)
	}
	if !contracts.IsConnectorQualityProbeField(instance.ID) || !contracts.IsConnectorQualityProbeField(strings.TrimSpace(instance.ConnectorID)) {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: connector identifiers are invalid", ErrUnverified)
	}
	connector, err := s.store.GetConnector(ctx, strings.TrimSpace(instance.ConnectorID))
	now := s.now().UTC()
	if err != nil || connector.UserID != instance.UserID || connector.InstanceID != instance.ID ||
		connector.Status != contracts.ConnectorStatusOnline || connector.LastSeenAt == nil || connector.LastSeenAt.Before(now.Add(-time.Minute)) ||
		!connectorSupportsBindingInstall(connector) {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: connector does not support secure binding installation", ErrUnverified)
	}
	secret, err := s.vault.Resolve(ctx, secretRef)
	if err != nil || secret.Value == "" || len(secret.Value) > contracts.ConnectorBindingEncryptionMaxPlaintext {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: binding secret is unavailable", ErrUnverified)
	}
	plaintext := secret.Value
	if baseURL != "" {
		encoded, marshalErr := json.Marshal(struct {
			Key     string `json:"key"`
			BaseURL string `json:"base_url"`
		}{Key: secret.Value, BaseURL: baseURL})
		if marshalErr != nil || len(encoded) > contracts.ConnectorBindingEncryptionMaxPlaintext {
			return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: supply binding is unavailable", ErrUnverified)
		}
		plaintext = string(encoded)
	}
	input := contracts.ConnectorGatewayBindingInstallInput{BindingID: bindingID, ChannelID: channelID, KeyVersion: keyVersion}
	input.Ciphertext, err = encryptBinding(plaintext, connector.ID, instance.ID, input, connector.Gateway.BindingEncryptionPublicKey, s.random)
	if err != nil {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: encrypt binding: %v", ErrUnverified, err)
	}
	result, err := s.installer.InstallBinding(ctx, instance.ID, input)
	if err != nil {
		return contracts.ConnectorGatewayBindingInstallResult{}, fmt.Errorf("%w: connector binding install failed: %v", ErrUnverified, err)
	}
	return result, nil
}

func deliveryAssignedToUser(delivery contracts.UpstreamKeyDelivery, assigned []contracts.AssignedUpstreamKey) bool {
	for _, key := range assigned {
		if key.ChannelID == delivery.ChannelID && key.ID == delivery.ID && key.KeyVersion == delivery.KeyVersion && key.SecretRef == delivery.SecretRef {
			return true
		}
	}
	return false
}

func connectorSupportsBindingInstall(connector contracts.Connector) bool {
	if !contracts.IsConnectorBindingEncryptionPublicKey(connector.Gateway.BindingEncryptionPublicKey) {
		return false
	}
	for _, capability := range connector.Gateway.Capabilities {
		if capability == contracts.ConnectorTaskGatewayBindingInstall {
			return true
		}
	}
	return false
}

type bindingCiphertextEnvelope struct {
	Algorithm          string `json:"algorithm"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Nonce              string `json:"nonce"`
	Sealed             string `json:"sealed"`
}

func encryptBinding(plaintext, connectorID, instanceID string, input contracts.ConnectorGatewayBindingInstallInput, publicKey string, random io.Reader) (string, error) {
	if len(plaintext) == 0 || len(plaintext) > contracts.ConnectorBindingEncryptionMaxPlaintext || random == nil {
		return "", errors.New("invalid binding plaintext")
	}
	publicBytes, err := base64.RawURLEncoding.DecodeString(publicKey)
	if err != nil || len(publicBytes) != contracts.ConnectorBindingEncryptionPublicKeySize || base64.RawURLEncoding.EncodeToString(publicBytes) != publicKey {
		return "", errors.New("invalid connector public key")
	}
	public, err := ecdh.X25519().NewPublicKey(publicBytes)
	if err != nil {
		return "", errors.New("invalid connector public key")
	}
	ephemeral, err := ecdh.X25519().GenerateKey(random)
	if err != nil {
		return "", err
	}
	shared, err := ephemeral.ECDH(public)
	if err != nil {
		return "", errors.New("binding key agreement failed")
	}
	key, err := hkdf.Key(sha256.New, shared, nil, contracts.ConnectorBindingEncryptionKeyDomain, 32)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return "", err
	}
	aad := contracts.ConnectorGatewayBindingEncryptionAAD(connectorID, instanceID, input.BindingID, input.ChannelID, input.KeyVersion)
	sealed := aead.Seal(nil, nonce, []byte(plaintext), aad)
	envelope := bindingCiphertextEnvelope{
		Algorithm:          contracts.ConnectorBindingEncryptionAlgorithm,
		EphemeralPublicKey: base64.RawURLEncoding.EncodeToString(ephemeral.PublicKey().Bytes()),
		Nonce:              base64.RawURLEncoding.EncodeToString(nonce), Sealed: base64.RawURLEncoding.EncodeToString(sealed),
	}
	raw, err := json.Marshal(envelope)
	return string(raw), err
}

func (s *Service) Verify(ctx context.Context, channelID, instanceID string) (Verification, error) {
	channelID = strings.TrimSpace(channelID)
	instanceID = strings.TrimSpace(instanceID)
	if s == nil || s.store == nil || s.vault == nil || s.prover == nil || channelID == "" || instanceID == "" {
		return Verification{}, ErrUnverified
	}
	channel, err := s.store.GetUpstreamChannel(ctx, channelID)
	if err != nil {
		return Verification{}, fmt.Errorf("%w: load channel: %v", ErrUnverified, err)
	}
	if channel.AccountOwnership.Normalize() != contracts.GatewayAccountPlatformManaged || strings.TrimSpace(channel.CredentialBindingID) == "" {
		return Verification{}, ErrUnverified
	}
	delivery, err := s.store.GetUpstreamKeyDelivery(ctx, channelID)
	if err != nil {
		return Verification{}, fmt.Errorf("%w: load delivery key: %v", ErrUnverified, err)
	}
	instance, err := s.store.GetInstance(ctx, instanceID)
	if err != nil || strings.TrimSpace(instance.ConnectorID) == "" {
		return Verification{Delivery: delivery}, fmt.Errorf("%w: connector is unavailable", ErrUnverified)
	}
	connectorID := strings.TrimSpace(instance.ConnectorID)
	connector, err := s.store.GetConnector(ctx, connectorID)
	now := s.now().UTC()
	if err != nil || connector.Status != contracts.ConnectorStatusOnline || connector.UserID != instance.UserID || connector.InstanceID != instance.ID ||
		connector.LastSeenAt == nil || connector.LastSeenAt.Before(now.Add(-time.Minute)) {
		s.markUnverified(ctx, delivery, instance.ID, connectorID)
		return Verification{Delivery: delivery}, fmt.Errorf("%w: connector is offline or stale", ErrUnverified)
	}
	secret, err := s.vault.Resolve(ctx, delivery.SecretRef)
	if err != nil || secret.Value == "" || len(secret.Value) > contracts.ConnectorBindingEncryptionMaxPlaintext {
		s.markUnverified(ctx, delivery, instance.ID, connectorID)
		return Verification{Delivery: delivery}, fmt.Errorf("%w: delivery secret is unavailable", ErrUnverified)
	}

	challengeBytes := make([]byte, contracts.ConnectorGatewayBindingProofChallengeHexLength/2)
	if _, err := io.ReadFull(s.random, challengeBytes); err != nil {
		return Verification{Delivery: delivery}, fmt.Errorf("%w: create challenge: %v", ErrUnverified, err)
	}
	input := contracts.ConnectorGatewayBindingProofInput{
		ChannelID: channel.ID,
		BindingID: strings.TrimSpace(channel.CredentialBindingID),
		Challenge: hex.EncodeToString(challengeBytes),
	}
	result, err := s.prover.ProveBinding(ctx, instance.ID, input)
	if err != nil {
		s.markUnverified(ctx, delivery, instance.ID, connectorID)
		return Verification{Delivery: delivery}, fmt.Errorf("%w: connector proof failed: %v", ErrUnverified, err)
	}

	mac := hmac.New(sha256.New, []byte(secret.Value))
	_, _ = mac.Write(contracts.ConnectorGatewayBindingProofMessage(input))
	expected := mac.Sum(nil)
	actual, decodeErr := hex.DecodeString(result.Proof)
	status := contracts.DeliveryKeyProofVerified
	if decodeErr != nil || len(actual) != sha256.Size || !hmac.Equal(actual, expected) {
		status = contracts.DeliveryKeyProofMismatch
	}
	previous, previousErr := s.store.GetUpstreamKeyProofReceipt(ctx, channel.ID, instance.ID)
	receipt, err := s.store.UpsertUpstreamKeyProofReceipt(ctx, contracts.UpstreamKeyProofReceipt{
		ChannelID: channel.ID, InstanceID: instance.ID, KeyVersion: delivery.KeyVersion,
		ConnectorID: connectorID, Status: status,
	})
	if err != nil {
		return Verification{Delivery: delivery}, fmt.Errorf("%w: persist proof: %v", ErrUnverified, err)
	}
	// Retain the delivery-level fields as a compatibility summary only. A
	// failure here cannot invalidate the authoritative per-instance receipt.
	updated := delivery
	if summary, summaryErr := s.store.UpdateUpstreamKeyDeliveryProof(ctx, channel.ID, delivery.KeyVersion, connectorID, status); summaryErr == nil {
		updated = summary
	}
	verification := Verification{
		Delivery: updated, Proof: receipt,
		FreshlyVerified: status == contracts.DeliveryKeyProofVerified &&
			(errors.Is(previousErr, store.ErrNotFound) || previous.Status != contracts.DeliveryKeyProofVerified ||
				previous.KeyVersion != delivery.KeyVersion || previous.ConnectorID != connectorID),
	}
	if status == contracts.DeliveryKeyProofMismatch {
		return verification, ErrMismatch
	}
	deployment, deploymentErr := s.store.GetUpstreamKeyDeployment(ctx, channel.ID, instance.ID)
	switch {
	case deploymentErr == nil:
		verification.DeploymentRequired = deployment.KeyVersion != updated.KeyVersion ||
			deployment.ConnectorID != connectorID || deployment.Status != contracts.DeliveryKeyDeploymentDeployed
	case errors.Is(deploymentErr, store.ErrNotFound):
		verification.DeploymentRequired = true
	default:
		return verification, fmt.Errorf("%w: load deployment acknowledgement: %v", ErrUnverified, deploymentErr)
	}
	return verification, nil
}

func (s *Service) markUnverified(ctx context.Context, delivery contracts.UpstreamKeyDelivery, instanceID, connectorID string) {
	if instanceID == "" || connectorID == "" || delivery.KeyVersion <= 0 {
		return
	}
	_, _ = s.store.UpsertUpstreamKeyProofReceipt(ctx, contracts.UpstreamKeyProofReceipt{
		ChannelID: delivery.ChannelID, InstanceID: instanceID, KeyVersion: delivery.KeyVersion,
		ConnectorID: connectorID, Status: contracts.DeliveryKeyProofUnverified,
	})
	_, _ = s.store.UpdateUpstreamKeyDeliveryProof(ctx, delivery.ChannelID, delivery.KeyVersion, connectorID, contracts.DeliveryKeyProofUnverified)
}
