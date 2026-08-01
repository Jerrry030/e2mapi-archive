package connector

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	UpstreamIntelligenceSourcesFilename = "upstream-intelligence-sources.json"

	defaultUpstreamIntelligencePollIntervalSeconds = 300
	minUpstreamIntelligencePollIntervalSeconds     = 60
	maxUpstreamIntelligencePollIntervalSeconds     = 3600
	maxUpstreamIntelligenceSources                 = 64
	maxUpstreamIntelligenceSourceRecords           = 256
	maxUpstreamIntelligenceDisplayNameBytes        = 120
	maxUpstreamIntelligenceURLBytes                = 2048
	maxUpstreamIntelligenceCredentialBytes         = 8192
	maxUpstreamIntelligenceSourcesFileBytes        = 4 << 20
	legacyUpstreamIntelligenceSourcesFileVersion   = 1
	upstreamIntelligenceSourcesFileVersion         = 2
)

type UpstreamIntelligenceSourceMode string
type UpstreamIntelligenceSourceStatus string

const (
	UpstreamIntelligenceSourceOwned    UpstreamIntelligenceSourceMode = "owned"
	UpstreamIntelligenceSourceExternal UpstreamIntelligenceSourceMode = "external"

	UpstreamIntelligenceSourceActive     UpstreamIntelligenceSourceStatus = "active"
	UpstreamIntelligenceSourcePaused     UpstreamIntelligenceSourceStatus = "paused"
	UpstreamIntelligenceSourceTombstoned UpstreamIntelligenceSourceStatus = "tombstoned"
)

type UpstreamIntelligenceSourceCredentials struct {
	// XAPIKey is retained only for the existing administrator reachability
	// check. Intelligence collection uses the independently issued user bearer
	// token and must never substitute one credential for the other.
	XAPIKey         string `json:"x_api_key,omitempty"`
	UserBearerToken string `json:"user_bearer_token,omitempty"`
}

// UpstreamIntelligenceSource is Connector-private. It must never be returned
// directly from an HTTP handler because Credentials are deliberately
// write-only at the loopback API boundary.
type UpstreamIntelligenceSource struct {
	LocalRef            string                                `json:"local_ref"`
	Mode                UpstreamIntelligenceSourceMode        `json:"mode"`
	Provider            string                                `json:"provider"`
	DisplayName         string                                `json:"display_name"`
	GatewayURL          string                                `json:"gateway_url,omitempty"`
	Credentials         UpstreamIntelligenceSourceCredentials `json:"credentials,omitempty"`
	Currency            string                                `json:"currency,omitempty"`
	PollIntervalSeconds int                                   `json:"poll_interval_seconds"`
	Status              UpstreamIntelligenceSourceStatus      `json:"status"`
	CreatedAt           time.Time                             `json:"created_at"`
	UpdatedAt           time.Time                             `json:"updated_at"`
	TombstonedAt        *time.Time                            `json:"tombstoned_at,omitempty"`
}

type UpstreamIntelligenceSourcePublic struct {
	LocalRef            string                           `json:"local_ref"`
	Mode                UpstreamIntelligenceSourceMode   `json:"mode"`
	Provider            string                           `json:"provider"`
	DisplayName         string                           `json:"display_name"`
	GatewayURL          string                           `json:"gateway_url,omitempty"`
	Currency            string                           `json:"currency,omitempty"`
	PollIntervalSeconds int                              `json:"poll_interval_seconds"`
	Status              UpstreamIntelligenceSourceStatus `json:"status"`
	HasUserBearerToken  bool                             `json:"has_user_bearer_token"`
	HasCredentials      bool                             `json:"has_credentials"`
	CreatedAt           time.Time                        `json:"created_at"`
	UpdatedAt           time.Time                        `json:"updated_at"`
	TombstonedAt        *time.Time                       `json:"tombstoned_at,omitempty"`
}

func (source UpstreamIntelligenceSource) Public() UpstreamIntelligenceSourcePublic {
	hasUserBearerToken := source.Credentials.UserBearerToken != ""
	return UpstreamIntelligenceSourcePublic{
		LocalRef:            source.LocalRef,
		Mode:                source.Mode,
		Provider:            source.Provider,
		DisplayName:         source.DisplayName,
		GatewayURL:          source.GatewayURL,
		Currency:            source.Currency,
		PollIntervalSeconds: source.PollIntervalSeconds,
		Status:              source.Status,
		HasUserBearerToken:  hasUserBearerToken,
		HasCredentials:      hasUserBearerToken || source.Credentials.XAPIKey != "",
		CreatedAt:           source.CreatedAt,
		UpdatedAt:           source.UpdatedAt,
		TombstonedAt:        source.TombstonedAt,
	}
}

type UpstreamIntelligenceSourceCreate struct {
	Mode                UpstreamIntelligenceSourceMode
	Provider            string
	DisplayName         string
	GatewayURL          string
	Credentials         UpstreamIntelligenceSourceCredentials
	Currency            string
	PollIntervalSeconds int
	Status              UpstreamIntelligenceSourceStatus
}

type UpstreamIntelligenceSourcePatch struct {
	DisplayName          *string
	GatewayURL           *string
	Credentials          UpstreamIntelligenceSourceCredentials
	ClearXAPIKey         bool
	ClearUserBearerToken bool
	Currency             *string
	PollIntervalSeconds  *int
	Status               *UpstreamIntelligenceSourceStatus
}

type upstreamIntelligenceSourcesPayload struct {
	Version int                          `json:"version"`
	Sources []UpstreamIntelligenceSource `json:"sources"`
}

type upstreamIntelligenceSourcesFile struct {
	Version  int                          `json:"version"`
	Sources  []UpstreamIntelligenceSource `json:"sources"`
	Checksum string                       `json:"checksum"`
}

type UpstreamIntelligenceSourceStore struct {
	path string
	mu   *sync.Mutex
}

var upstreamIntelligenceSourceStoreLocks sync.Map

func upstreamIntelligenceSourceStoreLock(path string) *sync.Mutex {
	value, _ := upstreamIntelligenceSourceStoreLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func NewUpstreamIntelligenceSourceStore(dataDir string) *UpstreamIntelligenceSourceStore {
	path := filepath.Join(dataDir, UpstreamIntelligenceSourcesFilename)
	return &UpstreamIntelligenceSourceStore{path: path, mu: upstreamIntelligenceSourceStoreLock(path)}
}

func (s *UpstreamIntelligenceSourceStore) List() ([]UpstreamIntelligenceSource, error) {
	if s == nil {
		return nil, errors.New("upstream intelligence source store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sources, err := s.loadLocked()
	if errors.Is(err, os.ErrNotExist) {
		return []UpstreamIntelligenceSource{}, nil
	}
	return sources, err
}

func (s *UpstreamIntelligenceSourceStore) Get(localRef string) (UpstreamIntelligenceSource, error) {
	if s == nil {
		return UpstreamIntelligenceSource{}, errors.New("upstream intelligence source store is not configured")
	}
	localRef = strings.TrimSpace(localRef)
	if !validUpstreamIntelligenceLocalRef(localRef) {
		return UpstreamIntelligenceSource{}, os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sources, err := s.loadLocked()
	if err != nil {
		return UpstreamIntelligenceSource{}, err
	}
	for _, source := range sources {
		if source.LocalRef == localRef {
			return source, nil
		}
	}
	return UpstreamIntelligenceSource{}, os.ErrNotExist
}

func (s *UpstreamIntelligenceSourceStore) Create(input UpstreamIntelligenceSourceCreate) (UpstreamIntelligenceSource, error) {
	if s == nil {
		return UpstreamIntelligenceSource{}, errors.New("upstream intelligence source store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sources, err := s.loadLocked()
	if errors.Is(err, os.ErrNotExist) {
		sources = []UpstreamIntelligenceSource{}
	} else if err != nil {
		return UpstreamIntelligenceSource{}, err
	}
	if len(sources) >= maxUpstreamIntelligenceSourceRecords {
		return UpstreamIntelligenceSource{}, errors.New("too many upstream intelligence source records")
	}
	normalizedMode := UpstreamIntelligenceSourceMode(strings.ToLower(strings.TrimSpace(string(input.Mode))))
	liveCount := 0
	for _, source := range sources {
		if source.Status != UpstreamIntelligenceSourceTombstoned {
			liveCount++
			if normalizedMode == UpstreamIntelligenceSourceOwned && source.Mode == UpstreamIntelligenceSourceOwned {
				return UpstreamIntelligenceSource{}, errors.New("an owned upstream intelligence source already exists")
			}
		}
	}
	if liveCount >= maxUpstreamIntelligenceSources {
		return UpstreamIntelligenceSource{}, errors.New("too many active upstream intelligence sources")
	}
	now := time.Now().UTC()
	localRef, err := newUpstreamIntelligenceLocalRef()
	if err != nil {
		return UpstreamIntelligenceSource{}, errors.New("generate upstream intelligence source reference")
	}
	source := UpstreamIntelligenceSource{
		LocalRef:            localRef,
		Mode:                normalizedMode,
		Provider:            input.Provider,
		DisplayName:         input.DisplayName,
		GatewayURL:          input.GatewayURL,
		Credentials:         input.Credentials,
		Currency:            input.Currency,
		PollIntervalSeconds: input.PollIntervalSeconds,
		Status:              input.Status,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	normalizeUpstreamIntelligenceSource(&source)
	if source.Status == "" {
		source.Status = UpstreamIntelligenceSourceActive
	}
	if err := validateUpstreamIntelligenceSource(source); err != nil {
		return UpstreamIntelligenceSource{}, err
	}
	sources = append(sources, source)
	if err := s.writeLocked(sources); err != nil {
		return UpstreamIntelligenceSource{}, err
	}
	return source, nil
}

func (s *UpstreamIntelligenceSourceStore) Patch(localRef string, patch UpstreamIntelligenceSourcePatch) (UpstreamIntelligenceSource, error) {
	if s == nil {
		return UpstreamIntelligenceSource{}, errors.New("upstream intelligence source store is not configured")
	}
	localRef = strings.TrimSpace(localRef)
	if !validUpstreamIntelligenceLocalRef(localRef) {
		return UpstreamIntelligenceSource{}, os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sources, err := s.loadLocked()
	if err != nil {
		return UpstreamIntelligenceSource{}, err
	}
	for index := range sources {
		current := sources[index]
		if current.LocalRef != localRef {
			continue
		}
		if current.Status == UpstreamIntelligenceSourceTombstoned {
			return UpstreamIntelligenceSource{}, errors.New("tombstoned upstream intelligence sources cannot be changed")
		}
		candidate := current
		if patch.DisplayName != nil {
			candidate.DisplayName = *patch.DisplayName
		}
		if patch.GatewayURL != nil {
			candidate.GatewayURL = *patch.GatewayURL
		}
		if patch.Currency != nil {
			candidate.Currency = *patch.Currency
		}
		if patch.PollIntervalSeconds != nil {
			candidate.PollIntervalSeconds = *patch.PollIntervalSeconds
		}
		if patch.Status != nil {
			candidate.Status = *patch.Status
		}
		freshXAPIKey := strings.TrimSpace(patch.Credentials.XAPIKey)
		freshUserBearerToken := strings.TrimSpace(patch.Credentials.UserBearerToken)
		if current.Mode == UpstreamIntelligenceSourceExternal &&
			patch.GatewayURL != nil && !sameUpstreamIntelligenceCredentialScope(current.GatewayURL, candidate.GatewayURL) {
			candidate.Credentials = UpstreamIntelligenceSourceCredentials{}
		}
		if freshXAPIKey != "" {
			candidate.Credentials.XAPIKey = freshXAPIKey
		}
		if freshUserBearerToken != "" {
			candidate.Credentials.UserBearerToken = freshUserBearerToken
		}
		if patch.ClearXAPIKey {
			candidate.Credentials.XAPIKey = ""
		}
		if patch.ClearUserBearerToken {
			candidate.Credentials.UserBearerToken = ""
			candidate.Status = UpstreamIntelligenceSourcePaused
		}
		candidate.UpdatedAt = time.Now().UTC()
		normalizeUpstreamIntelligenceSource(&candidate)
		if err := validateUpstreamIntelligenceSource(candidate); err != nil {
			return UpstreamIntelligenceSource{}, err
		}
		sources[index] = candidate
		if err := s.writeLocked(sources); err != nil {
			return UpstreamIntelligenceSource{}, err
		}
		return candidate, nil
	}
	return UpstreamIntelligenceSource{}, os.ErrNotExist
}

func (s *UpstreamIntelligenceSourceStore) Tombstone(localRef string) (UpstreamIntelligenceSource, error) {
	if s == nil {
		return UpstreamIntelligenceSource{}, errors.New("upstream intelligence source store is not configured")
	}
	localRef = strings.TrimSpace(localRef)
	if !validUpstreamIntelligenceLocalRef(localRef) {
		return UpstreamIntelligenceSource{}, os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sources, err := s.loadLocked()
	if err != nil {
		return UpstreamIntelligenceSource{}, err
	}
	for index := range sources {
		if sources[index].LocalRef != localRef {
			continue
		}
		if sources[index].Status == UpstreamIntelligenceSourceTombstoned {
			return sources[index], nil
		}
		now := time.Now().UTC()
		sources[index].Status = UpstreamIntelligenceSourceTombstoned
		sources[index].Credentials = UpstreamIntelligenceSourceCredentials{}
		sources[index].UpdatedAt = now
		sources[index].TombstonedAt = &now
		if err := s.writeLocked(sources); err != nil {
			return UpstreamIntelligenceSource{}, err
		}
		return sources[index], nil
	}
	return UpstreamIntelligenceSource{}, os.ErrNotExist
}

func (s *UpstreamIntelligenceSourceStore) loadLocked() ([]UpstreamIntelligenceSource, error) {
	raw, err := readRegularFileNoSymlink(s.path)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxUpstreamIntelligenceSourcesFileBytes {
		return nil, errors.New("upstream intelligence source file exceeds 4 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var stored upstreamIntelligenceSourcesFile
	if err := decoder.Decode(&stored); err != nil {
		return nil, fmt.Errorf("decode upstream intelligence sources: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return nil, errors.New("decode upstream intelligence sources: multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode upstream intelligence sources: %w", err)
	}
	if stored.Version != legacyUpstreamIntelligenceSourcesFileVersion &&
		stored.Version != upstreamIntelligenceSourcesFileVersion {
		return nil, errors.New("unsupported upstream intelligence source file version")
	}
	want, err := upstreamIntelligenceSourcesChecksum(stored.Version, stored.Sources)
	if err != nil {
		return nil, err
	}
	if len(stored.Checksum) != len(want) || !strings.EqualFold(stored.Checksum, want) {
		return nil, errors.New("upstream intelligence source file checksum mismatch")
	}
	if len(stored.Sources) > maxUpstreamIntelligenceSourceRecords {
		return nil, errors.New("too many upstream intelligence source records")
	}
	seen := make(map[string]struct{}, len(stored.Sources))
	for index := range stored.Sources {
		normalizeUpstreamIntelligenceSource(&stored.Sources[index])
		// Version 1 files predate user bearer credentials and treated an
		// administrator x-api-key as sufficient to activate a source. Never
		// reinterpret that more privileged credential as a user token: legacy
		// sources remain available but are paused until a bearer is explicitly
		// configured and the operator reactivates them.
		if stored.Version == legacyUpstreamIntelligenceSourcesFileVersion &&
			stored.Sources[index].Credentials.UserBearerToken != "" {
			return nil, errors.New("legacy upstream intelligence source file contains unsupported user credentials")
		}
		if stored.Version == legacyUpstreamIntelligenceSourcesFileVersion &&
			stored.Sources[index].Status == UpstreamIntelligenceSourceActive &&
			stored.Sources[index].Credentials.UserBearerToken == "" {
			stored.Sources[index].Status = UpstreamIntelligenceSourcePaused
		}
		if _, ok := seen[stored.Sources[index].LocalRef]; ok {
			return nil, errors.New("duplicate upstream intelligence source reference")
		}
		seen[stored.Sources[index].LocalRef] = struct{}{}
		if err := validateUpstreamIntelligenceSource(stored.Sources[index]); err != nil {
			return nil, fmt.Errorf("invalid upstream intelligence source record: %w", err)
		}
	}
	return stored.Sources, nil
}

func (s *UpstreamIntelligenceSourceStore) writeLocked(sources []UpstreamIntelligenceSource) error {
	copySources := append([]UpstreamIntelligenceSource(nil), sources...)
	sort.SliceStable(copySources, func(left, right int) bool {
		return copySources[left].LocalRef < copySources[right].LocalRef
	})
	checksum, err := upstreamIntelligenceSourcesChecksum(upstreamIntelligenceSourcesFileVersion, copySources)
	if err != nil {
		return err
	}
	stored := upstreamIntelligenceSourcesFile{
		Version:  upstreamIntelligenceSourcesFileVersion,
		Sources:  copySources,
		Checksum: checksum,
	}
	raw, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	if len(raw)+1 > maxUpstreamIntelligenceSourcesFileBytes {
		return errors.New("upstream intelligence source file exceeds 4 MiB")
	}
	return atomicWritePrivateFile(s.path, append(raw, '\n'))
}

func upstreamIntelligenceSourcesChecksum(version int, sources []UpstreamIntelligenceSource) (string, error) {
	raw, err := json.Marshal(upstreamIntelligenceSourcesPayload{Version: version, Sources: sources})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func normalizeUpstreamIntelligenceSource(source *UpstreamIntelligenceSource) {
	source.LocalRef = strings.TrimSpace(source.LocalRef)
	source.Mode = UpstreamIntelligenceSourceMode(strings.ToLower(strings.TrimSpace(string(source.Mode))))
	source.Provider = strings.ToLower(strings.TrimSpace(source.Provider))
	if source.Provider == "" {
		source.Provider = "sub2api"
	}
	source.DisplayName = strings.TrimSpace(source.DisplayName)
	source.GatewayURL = strings.TrimRight(strings.TrimSpace(source.GatewayURL), "/")
	source.Credentials.XAPIKey = strings.TrimSpace(source.Credentials.XAPIKey)
	source.Credentials.UserBearerToken = strings.TrimSpace(source.Credentials.UserBearerToken)
	source.Currency = strings.ToUpper(strings.TrimSpace(source.Currency))
	if source.PollIntervalSeconds == 0 {
		source.PollIntervalSeconds = defaultUpstreamIntelligencePollIntervalSeconds
	}
	source.Status = UpstreamIntelligenceSourceStatus(strings.ToLower(strings.TrimSpace(string(source.Status))))
}

func validateUpstreamIntelligenceSource(source UpstreamIntelligenceSource) error {
	if !validUpstreamIntelligenceLocalRef(source.LocalRef) {
		return errors.New("invalid upstream intelligence source reference")
	}
	if source.Mode != UpstreamIntelligenceSourceOwned && source.Mode != UpstreamIntelligenceSourceExternal {
		return errors.New("source mode must be owned or external")
	}
	if source.Provider != "sub2api" {
		return errors.New("source provider must be sub2api")
	}
	if source.DisplayName == "" || len(source.DisplayName) > maxUpstreamIntelligenceDisplayNameBytes ||
		!utf8.ValidString(source.DisplayName) || strings.IndexFunc(source.DisplayName, unicode.IsControl) >= 0 {
		return fmt.Errorf("display name must be between 1 and %d bytes", maxUpstreamIntelligenceDisplayNameBytes)
	}
	if source.PollIntervalSeconds < minUpstreamIntelligencePollIntervalSeconds ||
		source.PollIntervalSeconds > maxUpstreamIntelligencePollIntervalSeconds {
		return fmt.Errorf("poll interval must be between %d and %d seconds", minUpstreamIntelligencePollIntervalSeconds, maxUpstreamIntelligencePollIntervalSeconds)
	}
	if source.Currency != "" && !validUpstreamIntelligenceCurrency(source.Currency) {
		return errors.New("currency must be a three-letter ISO 4217 code")
	}
	switch source.Status {
	case UpstreamIntelligenceSourceActive, UpstreamIntelligenceSourcePaused:
		if source.TombstonedAt != nil {
			return errors.New("live source must not have a tombstone timestamp")
		}
	case UpstreamIntelligenceSourceTombstoned:
		if source.TombstonedAt == nil || source.Credentials.XAPIKey != "" || source.Credentials.UserBearerToken != "" {
			return errors.New("tombstoned source must have a timestamp and no credentials")
		}
	default:
		return errors.New("source status must be active, paused, or tombstoned")
	}
	if source.CreatedAt.IsZero() || source.UpdatedAt.IsZero() || source.UpdatedAt.Before(source.CreatedAt) {
		return errors.New("source timestamps are invalid")
	}
	if len(source.Credentials.XAPIKey) > maxUpstreamIntelligenceCredentialBytes ||
		len(source.Credentials.UserBearerToken) > maxUpstreamIntelligenceCredentialBytes {
		return errors.New("source credential is too large")
	}
	switch source.Mode {
	case UpstreamIntelligenceSourceOwned:
		if source.GatewayURL != "" || source.Credentials.XAPIKey != "" {
			return errors.New("owned source must reuse only the managed gateway URL")
		}
		if source.Status == UpstreamIntelligenceSourceActive && source.Credentials.UserBearerToken == "" {
			return errors.New("user bearer credential is required for an active owned source")
		}
	case UpstreamIntelligenceSourceExternal:
		if len(source.GatewayURL) > maxUpstreamIntelligenceURLBytes {
			return errors.New("gateway URL is too large")
		}
		gateway := GatewayLocalConfig{
			GatewayKind: "sub2api",
			GatewayURL:  source.GatewayURL,
			Auth:        GatewayAuthXAPIKey,
		}
		gateway.Normalize()
		if err := validateGatewayLocalConfigBase(gateway); err != nil {
			return err
		}
		if source.Status == UpstreamIntelligenceSourceActive && source.Credentials.UserBearerToken == "" {
			return errors.New("user bearer credential is required for an active external source")
		}
	}
	return nil
}

func testUpstreamIntelligenceSource(ctx context.Context, client *http.Client, managedStore *LocalConfigStore, source UpstreamIntelligenceSource) (int, error) {
	if source.Status == UpstreamIntelligenceSourceTombstoned {
		return 0, errors.New("source is tombstoned")
	}
	var cfg GatewayLocalConfig
	switch source.Mode {
	case UpstreamIntelligenceSourceOwned:
		if managedStore == nil {
			return 0, errors.New("managed gateway config is unavailable")
		}
		loaded, err := managedStore.Load()
		if err != nil {
			return 0, errors.New("managed gateway config is unavailable")
		}
		if loaded.GatewayKind != "sub2api" {
			return 0, errors.New("managed gateway is not sub2api")
		}
		cfg = loaded
	case UpstreamIntelligenceSourceExternal:
		cfg = GatewayLocalConfig{
			GatewayKind: "sub2api",
			GatewayURL:  source.GatewayURL,
			Auth:        GatewayAuthXAPIKey,
			Credentials: GatewayLocalCredentials{XAPIKey: source.Credentials.XAPIKey},
		}
	default:
		return 0, errors.New("unsupported source mode")
	}
	// This is intentionally a bounded authenticated reachability check against
	// the existing safe Sub2API list endpoint. It does not parse facts and must
	// not be represented as an intelligence collection run.
	return TestGatewayLocalConfig(ctx, client, cfg)
}

var upstreamIntelligenceLocalRefPattern = regexp.MustCompile(`^uis_[0-9a-f]{32}$`)

func newUpstreamIntelligenceLocalRef() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "uis_" + hex.EncodeToString(raw), nil
}

func validUpstreamIntelligenceLocalRef(value string) bool {
	return upstreamIntelligenceLocalRefPattern.MatchString(value)
}

func validUpstreamIntelligenceCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, char := range value {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}

func sameUpstreamIntelligenceCredentialScope(left, right string) bool {
	leftOrigin, leftOK := normalizedGatewayOrigin(left)
	rightOrigin, rightOK := normalizedGatewayOrigin(right)
	return leftOK && rightOK && leftOrigin == rightOrigin
}
