package connector

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"e2m.local/contracts"
)

const (
	ConnectorBindingKeyFilename  = "binding-encryption-key.json"
	bindingInstallLedgerFilename = "binding-install-ledger.json"
	bindingEncryptionKeyVersion  = 1
	bindingInstallLedgerVersion  = 1
)

type BindingEncryptionIdentity struct {
	private *ecdh.PrivateKey
}

type persistedBindingEncryptionKey struct {
	Version    int    `json:"version"`
	Algorithm  string `json:"algorithm"`
	PrivateKey string `json:"private_key"`
}

func EnsureBindingEncryptionIdentity(dataDir string) (*BindingEncryptionIdentity, error) {
	path := filepath.Join(strings.TrimSpace(dataDir), ConnectorBindingKeyFilename)
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("binding encryption data directory is required")
	}
	identity, err := loadBindingEncryptionIdentity(path)
	if err == nil {
		return identity, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load binding encryption identity: %w", err)
	}
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate binding encryption identity: %w", err)
	}
	raw, err := json.MarshalIndent(persistedBindingEncryptionKey{
		Version: bindingEncryptionKeyVersion, Algorithm: contracts.ConnectorBindingEncryptionAlgorithm,
		PrivateKey: base64.RawURLEncoding.EncodeToString(private.Bytes()),
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := atomicWritePrivateFile(path, append(raw, '\n')); err != nil {
		return nil, fmt.Errorf("persist binding encryption identity: %w", err)
	}
	return loadBindingEncryptionIdentity(path)
}

func loadBindingEncryptionIdentity(path string) (*BindingEncryptionIdentity, error) {
	raw, err := readRegularFileNoSymlink(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var stored persistedBindingEncryptionKey
	if err := decoder.Decode(&stored); err != nil {
		return nil, fmt.Errorf("decode binding encryption identity: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode binding encryption identity: trailing JSON")
	}
	if stored.Version != bindingEncryptionKeyVersion || stored.Algorithm != contracts.ConnectorBindingEncryptionAlgorithm {
		return nil, errors.New("binding encryption identity version or algorithm is unsupported")
	}
	privateBytes, err := base64.RawURLEncoding.DecodeString(stored.PrivateKey)
	if err != nil || base64.RawURLEncoding.EncodeToString(privateBytes) != stored.PrivateKey {
		return nil, errors.New("binding encryption private key is invalid")
	}
	private, err := ecdh.X25519().NewPrivateKey(privateBytes)
	if err != nil {
		return nil, errors.New("binding encryption private key is invalid")
	}
	return &BindingEncryptionIdentity{private: private}, nil
}

func (i *BindingEncryptionIdentity) PublicKey() string {
	if i == nil || i.private == nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(i.private.PublicKey().Bytes())
}

type bindingCiphertextEnvelope struct {
	Algorithm          string `json:"algorithm"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Nonce              string `json:"nonce"`
	Sealed             string `json:"sealed"`
}

func (i *BindingEncryptionIdentity) decrypt(input contracts.ConnectorGatewayBindingInstallInput, connectorID, instanceID string) ([]byte, error) {
	if i == nil || i.private == nil || !contracts.IsConnectorBindingCiphertext(input.Ciphertext) {
		return nil, errors.New("binding ciphertext is unavailable or too large")
	}
	var envelope bindingCiphertextEnvelope
	decoder := json.NewDecoder(strings.NewReader(input.Ciphertext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, errors.New("binding ciphertext envelope is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || envelope.Algorithm != contracts.ConnectorBindingEncryptionAlgorithm {
		return nil, errors.New("binding ciphertext envelope is unsupported")
	}
	ephemeralBytes, err := decodeCanonicalBase64(envelope.EphemeralPublicKey, contracts.ConnectorBindingEncryptionPublicKeySize)
	if err != nil {
		return nil, errors.New("binding ciphertext public key is invalid")
	}
	nonce, err := decodeCanonicalBase64(envelope.Nonce, contracts.ConnectorBindingEncryptionNonceSize)
	if err != nil {
		return nil, errors.New("binding ciphertext nonce is invalid")
	}
	sealed, err := decodeCanonicalBase64(envelope.Sealed, -1)
	if err != nil || len(sealed) < 16 || len(sealed) > contracts.ConnectorBindingEncryptionMaxCiphertext {
		return nil, errors.New("binding ciphertext payload is invalid")
	}
	ephemeral, err := ecdh.X25519().NewPublicKey(ephemeralBytes)
	if err != nil {
		return nil, errors.New("binding ciphertext public key is invalid")
	}
	shared, err := i.private.ECDH(ephemeral)
	if err != nil {
		return nil, errors.New("binding ciphertext key agreement failed")
	}
	key, err := hkdf.Key(sha256.New, shared, nil, contracts.ConnectorBindingEncryptionKeyDomain, 32)
	if err != nil {
		return nil, errors.New("binding ciphertext key derivation failed")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("binding ciphertext cipher is unavailable")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("binding ciphertext cipher is unavailable")
	}
	aad := contracts.ConnectorGatewayBindingEncryptionAAD(connectorID, instanceID, input.BindingID, input.ChannelID, input.KeyVersion)
	plaintext, err := aead.Open(nil, nonce, sealed, aad)
	if err != nil || len(plaintext) == 0 || len(plaintext) > contracts.ConnectorBindingEncryptionMaxPlaintext {
		return nil, errors.New("binding ciphertext authentication failed")
	}
	return plaintext, nil
}

func decodeCanonicalBase64(value string, size int) ([]byte, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return nil, errors.New("invalid base64")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != value || size >= 0 && len(raw) != size {
		return nil, errors.New("invalid base64")
	}
	return raw, nil
}

func validBindingInstallInput(input contracts.ConnectorGatewayBindingInstallInput) bool {
	return input.BindingID == strings.TrimSpace(input.BindingID) &&
		input.ChannelID == strings.TrimSpace(input.ChannelID) &&
		contracts.IsConnectorQualityProbeField(input.BindingID) &&
		contracts.IsConnectorQualityProbeField(input.ChannelID) &&
		input.KeyVersion > 0 && contracts.IsConnectorBindingCiphertext(input.Ciphertext)
}

func (c *Connector) executeBindingInstall(task contracts.ConnectorTask) taskResult {
	var input contracts.ConnectorGatewayBindingInstallInput
	if err := json.Unmarshal(task.Input, &input); err != nil || !validBindingInstallInput(input) {
		return failedTask("invalid_task_input", "binding install input is invalid", false)
	}
	if c.cfg.BindingEncryption == nil || c.cfg.ConfigStore == nil || c.bindingInstallLedger == nil {
		return failedTask("gateway_config_unavailable", "binding installation is unavailable", true)
	}
	c.bindingInstallLedger.mu.Lock()
	defer c.bindingInstallLedger.mu.Unlock()
	alreadyInstalled, err := c.bindingInstallLedger.checkLocked(input.BindingID, input.ChannelID, input.KeyVersion)
	if err != nil {
		if strings.Contains(err.Error(), "stale") {
			return failedTask("binding_version_stale", "binding key version is stale", false)
		}
		if strings.Contains(err.Error(), "another channel") {
			return failedTask("idempotency_conflict", "binding id belongs to another channel", false)
		}
		return failedTask("gateway_config_unavailable", "binding install ledger is unavailable", true)
	}
	result := contracts.ConnectorGatewayBindingInstallResult{
		BindingID: input.BindingID, ChannelID: input.ChannelID, KeyVersion: input.KeyVersion,
	}
	if alreadyInstalled {
		if existing, err := c.cfg.ConfigStore.BindingResolver().ResolveBinding(nil, input.BindingID); err == nil {
			plaintext, decryptErr := c.cfg.BindingEncryption.decrypt(input, c.cfg.ConnectorID, c.cfg.InstanceID)
			if decryptErr != nil {
				return failedTask("invalid_task_input", "binding ciphertext is invalid", false)
			}
			matches := constantTimeStringEqual(existing, string(plaintext))
			for index := range plaintext {
				plaintext[index] = 0
			}
			if !matches {
				return failedTask("idempotency_conflict", "binding version was reused for different key material", false)
			}
			return gatewayResult(result, nil)
		} else if !errors.Is(err, os.ErrNotExist) {
			return failedTask("gateway_config_unavailable", "binding store is unavailable", true)
		}
	}
	plaintext, err := c.cfg.BindingEncryption.decrypt(input, c.cfg.ConnectorID, c.cfg.InstanceID)
	if err != nil {
		return failedTask("invalid_task_input", "binding ciphertext is invalid", false)
	}
	if len(bytes.TrimSpace(plaintext)) == 0 {
		for index := range plaintext {
			plaintext[index] = 0
		}
		return failedTask("invalid_task_input", "binding plaintext is invalid", false)
	}
	value := string(plaintext)
	if err := c.cfg.ConfigStore.BindingResolver().Merge(map[string]string{input.BindingID: value}); err != nil {
		for index := range plaintext {
			plaintext[index] = 0
		}
		return failedTask("gateway_config_unavailable", "binding store is unavailable", true)
	}
	for index := range plaintext {
		plaintext[index] = 0
	}
	if err := c.bindingInstallLedger.commitLocked(input.BindingID, input.ChannelID, input.KeyVersion); err != nil {
		return failedTask("gateway_config_unavailable", "binding install ledger is unavailable", true)
	}
	return gatewayResult(result, nil)
}

func constantTimeStringEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

type bindingInstallRecord struct {
	ChannelID  string `json:"channel_id"`
	KeyVersion int64  `json:"key_version"`
}

type persistedBindingInstallLedger struct {
	Version int                             `json:"version"`
	Records map[string]bindingInstallRecord `json:"records"`
}

type bindingInstallLedger struct {
	path string
	mu   sync.Mutex
}

func newBindingInstallLedger(dataDir string) *bindingInstallLedger {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &bindingInstallLedger{path: filepath.Join(dataDir, bindingInstallLedgerFilename)}
}

func (l *bindingInstallLedger) checkLocked(bindingID, channelID string, keyVersion int64) (bool, error) {
	if l == nil {
		return false, errors.New("binding install ledger is unavailable")
	}
	records, err := l.load()
	if err != nil {
		return false, err
	}
	record, exists := records[bindingID]
	if !exists {
		return false, nil
	}
	if record.ChannelID != channelID {
		return false, errors.New("binding id belongs to another channel")
	}
	if keyVersion < record.KeyVersion {
		return false, errors.New("binding key version is stale")
	}
	return keyVersion == record.KeyVersion, nil
}

func (l *bindingInstallLedger) commitLocked(bindingID, channelID string, keyVersion int64) error {
	if l == nil {
		return errors.New("binding install ledger is unavailable")
	}
	records, err := l.load()
	if err != nil {
		return err
	}
	if record, exists := records[bindingID]; exists {
		if record.ChannelID != channelID || keyVersion < record.KeyVersion {
			return errors.New("binding install ledger conflict")
		}
		if keyVersion == record.KeyVersion {
			return nil
		}
	}
	records[bindingID] = bindingInstallRecord{ChannelID: channelID, KeyVersion: keyVersion}
	raw, err := json.MarshalIndent(persistedBindingInstallLedger{Version: bindingInstallLedgerVersion, Records: records}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWritePrivateFile(l.path, append(raw, '\n'))
}

func (l *bindingInstallLedger) load() (map[string]bindingInstallRecord, error) {
	raw, err := readRegularFileNoSymlink(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bindingInstallRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var stored persistedBindingInstallLedger
	if err := decoder.Decode(&stored); err != nil {
		return nil, fmt.Errorf("decode binding install ledger: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || stored.Version != bindingInstallLedgerVersion || stored.Records == nil {
		return nil, errors.New("binding install ledger is invalid")
	}
	for bindingID, record := range stored.Records {
		if !contracts.IsConnectorQualityProbeField(bindingID) || !contracts.IsConnectorQualityProbeField(record.ChannelID) || record.KeyVersion <= 0 {
			return nil, errors.New("binding install ledger contains an invalid record")
		}
	}
	return stored.Records, nil
}
