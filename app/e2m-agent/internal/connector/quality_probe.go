package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"e2m.local/contracts"
)

type qualityProbeOutboxRecord struct {
	Signature string          `json:"signature"`
	Result    json.RawMessage `json:"result"`
}

func (c *Connector) reportQualityProbe(ctx context.Context, observation contracts.ConnectorChannelObservation) error {
	if err := c.reportObservationBatch(ctx, []contracts.ConnectorChannelObservation{observation}); err != nil {
		return fmt.Errorf("connector: report quality probe: %w", err)
	}
	return nil
}

func (c *Connector) qualityProbeObservation(task contracts.ConnectorTask, taskResult taskResult) (contracts.ConnectorChannelObservation, error) {
	var input contracts.ConnectorGatewayQualityProbeInput
	if err := json.Unmarshal(task.Input, &input); err != nil {
		return contracts.ConnectorChannelObservation{}, fmt.Errorf("connector: decode quality probe input: %w", err)
	}
	var result contracts.ConnectorGatewayQualityProbeResult
	if err := json.Unmarshal(taskResult.result, &result); err != nil {
		return contracts.ConnectorChannelObservation{}, fmt.Errorf("connector: decode quality probe result: %w", err)
	}
	if err := validateQualityProbeResult(result); err != nil {
		return contracts.ConnectorChannelObservation{}, fmt.Errorf("connector: invalid quality probe result: %w", err)
	}
	if input.Capability != result.Capability || input.EndpointPath != result.EndpointPath {
		return contracts.ConnectorChannelObservation{}, errors.New("connector: quality probe result scope does not match task input")
	}
	return contracts.ConnectorChannelObservation{
		ObservationID: qualityProbeObservationID(c.cfg.ConnectorID, task.ID),
		ChannelID:     strings.TrimSpace(input.ChannelID),
		RemoteID:      strings.TrimSpace(input.AccountID),
		Model:         strings.TrimSpace(input.Model),
		Capability:    result.Capability,
		EndpointPath:  result.EndpointPath,
		Success:       result.Success,
		StatusCode:    result.Status,
		ErrorType:     result.ErrorType,
		FirstTokenMS:  result.FirstTokenMS,
		TotalMS:       result.TotalMS,
		Source:        contracts.ObservationProbe,
		ObservedAt:    result.ObservedAt.UTC().Truncate(time.Microsecond),
	}, nil
}

func qualityProbeObservationID(connectorID, taskID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(connectorID) + "\x00" + strings.TrimSpace(taskID)))
	return "probe:v1:" + hex.EncodeToString(sum[:])
}

func validateQualityProbeResult(result contracts.ConnectorGatewayQualityProbeResult) error {
	if !contracts.IsQualityProbeCapability(result.Capability) || !contracts.IsQualityProbeEndpointPath(result.EndpointPath) {
		return errors.New("capability and endpoint_path are required")
	}
	if result.ObservedAt.IsZero() {
		return errors.New("observed_at is required")
	}
	if result.Status < 0 || result.Status > 599 {
		return errors.New("status must be between 0 and 599")
	}
	if result.FirstTokenMS < 0 || result.TotalMS < 0 || math.IsNaN(result.FirstTokenMS) || math.IsNaN(result.TotalMS) || math.IsInf(result.FirstTokenMS, 0) || math.IsInf(result.TotalMS, 0) {
		return errors.New("latency values must be finite and non-negative")
	}
	if result.FirstTokenMS > 0 && result.TotalMS > 0 && result.FirstTokenMS > result.TotalMS {
		return errors.New("first_token_ms cannot exceed total_ms")
	}
	if !validQualityProbeErrorType(result.ErrorType) {
		return errors.New("error_type is not supported")
	}
	if result.Success && result.ErrorType != contracts.ErrorNone {
		return errors.New("successful probe cannot include error_type")
	}
	if result.Success && (result.Status < http.StatusOK || result.Status >= http.StatusMultipleChoices) {
		return errors.New("successful probe requires a 2xx status")
	}
	if result.Success && (result.FirstTokenMS <= 0 || result.TotalMS <= 0) {
		return errors.New("successful probe requires first_token_ms and total_ms")
	}
	if !result.Success && result.ErrorType == contracts.ErrorNone {
		return errors.New("failed probe requires error_type")
	}
	return nil
}

func validQualityProbeErrorType(value contracts.ObservationErrorType) bool {
	switch value {
	case contracts.ErrorNone, contracts.ErrorTimeout, contracts.ErrorRateLimit,
		contracts.ErrorAuth, contracts.ErrorInsufficientBalance, contracts.ErrorServer,
		contracts.ErrorClient, contracts.ErrorNetwork, contracts.ErrorCanceled, contracts.ErrorPlatform,
		contracts.ErrorUnknown:
		return true
	default:
		return false
	}
}

func (c *Connector) persistQualityProbeOutbox(task contracts.ConnectorTask, result taskResult) error {
	path, err := c.qualityProbeOutboxPath(task)
	if err != nil {
		return err
	}
	record := qualityProbeOutboxRecord{Signature: taskSignature(task), Result: append(json.RawMessage(nil), result.result...)}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return atomicWritePrivateFile(path, append(raw, '\n'))
}

func (c *Connector) loadQualityProbeOutbox(task contracts.ConnectorTask) (taskResult, bool, error) {
	path, err := c.qualityProbeOutboxPath(task)
	if err != nil {
		return taskResult{}, false, err
	}
	raw, err := readRegularFileNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return taskResult{}, false, nil
	}
	if err != nil {
		return taskResult{}, false, err
	}
	var record qualityProbeOutboxRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return taskResult{}, false, err
	}
	if record.Signature != taskSignature(task) {
		return taskResult{}, false, errors.New("quality probe outbox signature mismatch")
	}
	var result contracts.ConnectorGatewayQualityProbeResult
	if err := json.Unmarshal(record.Result, &result); err != nil {
		return taskResult{}, false, err
	}
	if err := validateQualityProbeResult(result); err != nil {
		return taskResult{}, false, err
	}
	return taskResult{success: true, result: append(json.RawMessage(nil), record.Result...)}, true, nil
}

func (c *Connector) removeQualityProbeOutbox(task contracts.ConnectorTask) error {
	path, err := c.qualityProbeOutboxPath(task)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (c *Connector) qualityProbeOutboxPath(task contracts.ConnectorTask) (string, error) {
	if c.cfg.ConfigStore == nil || strings.TrimSpace(c.cfg.ConfigStore.path) == "" {
		return "", errors.New("connector local store is not configured")
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(c.cfg.ConnectorID) + "\x00" + strings.TrimSpace(task.ID)))
	dir := filepath.Join(filepath.Dir(c.cfg.ConfigStore.path), "quality-probe-outbox")
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), nil
}
