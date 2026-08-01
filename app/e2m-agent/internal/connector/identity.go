package connector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	ConnectorIdentityFilename = "connector-identity.json"
	connectorIdentityVersion  = 1
)

// ConnectorIdentity binds one private Connector data directory to exactly one
// Core Connector and gateway instance. The IDs are not credentials, but the
// binding must remain stable alongside the Connector token stored in that
// directory.
type ConnectorIdentity struct {
	ConnectorID string
	InstanceID  string
}

type persistedConnectorIdentity struct {
	Version     int    `json:"version"`
	ConnectorID string `json:"connector_id"`
	InstanceID  string `json:"instance_id"`
}

// ResolveConnectorIdentity initializes identityPath from the environment IDs,
// or restores the identity when both environment IDs are absent. Supplying a
// partial or conflicting pair is rejected so a persisted token cannot be used
// for a different Connector or instance by accident.
func ResolveConnectorIdentity(identityPath, configuredConnectorID, configuredInstanceID string) (ConnectorIdentity, error) {
	identityPath = strings.TrimSpace(identityPath)
	if identityPath == "" {
		return ConnectorIdentity{}, errors.New("connector identity path is required")
	}
	configured := ConnectorIdentity{
		ConnectorID: strings.TrimSpace(configuredConnectorID),
		InstanceID:  strings.TrimSpace(configuredInstanceID),
	}
	if (configured.ConnectorID == "") != (configured.InstanceID == "") {
		return ConnectorIdentity{}, errors.New("connector id and instance id must be configured together")
	}

	persisted, err := loadConnectorIdentity(identityPath)
	if err == nil {
		if configured.ConnectorID != "" && configured != persisted {
			return ConnectorIdentity{}, errors.New("configured connector identity conflicts with persisted identity")
		}
		return persisted, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ConnectorIdentity{}, fmt.Errorf("load persisted connector identity: %w", err)
	}
	if configured.ConnectorID == "" {
		return ConnectorIdentity{}, errors.New("connector id and instance id are required before identity has been initialized")
	}

	raw, err := json.MarshalIndent(persistedConnectorIdentity{
		Version:     connectorIdentityVersion,
		ConnectorID: configured.ConnectorID,
		InstanceID:  configured.InstanceID,
	}, "", "  ")
	if err != nil {
		return ConnectorIdentity{}, fmt.Errorf("encode connector identity: %w", err)
	}
	if err := atomicWritePrivateFile(identityPath, append(raw, '\n')); err != nil {
		return ConnectorIdentity{}, fmt.Errorf("persist connector identity: %w", err)
	}

	// Read back through the hardened path so startup never relies on a file that
	// cannot be safely restored after the next container replacement.
	persisted, err = loadConnectorIdentity(identityPath)
	if err != nil {
		return ConnectorIdentity{}, fmt.Errorf("verify persisted connector identity: %w", err)
	}
	if persisted != configured {
		return ConnectorIdentity{}, errors.New("persisted connector identity does not match configured identity")
	}
	return persisted, nil
}

func loadConnectorIdentity(path string) (ConnectorIdentity, error) {
	raw, err := readRegularFileNoSymlink(path)
	if err != nil {
		return ConnectorIdentity{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var stored persistedConnectorIdentity
	if err := decoder.Decode(&stored); err != nil {
		return ConnectorIdentity{}, fmt.Errorf("decode connector identity: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ConnectorIdentity{}, errors.New("decode connector identity: trailing JSON value")
		}
		return ConnectorIdentity{}, fmt.Errorf("decode connector identity trailing data: %w", err)
	}
	if stored.Version != connectorIdentityVersion {
		return ConnectorIdentity{}, fmt.Errorf("unsupported connector identity version %d", stored.Version)
	}
	identity := ConnectorIdentity{
		ConnectorID: strings.TrimSpace(stored.ConnectorID),
		InstanceID:  strings.TrimSpace(stored.InstanceID),
	}
	if identity.ConnectorID == "" || identity.InstanceID == "" {
		return ConnectorIdentity{}, errors.New("persisted connector identity is incomplete")
	}
	if identity.ConnectorID != stored.ConnectorID || identity.InstanceID != stored.InstanceID {
		return ConnectorIdentity{}, errors.New("persisted connector identity is not normalized")
	}
	return identity, nil
}
