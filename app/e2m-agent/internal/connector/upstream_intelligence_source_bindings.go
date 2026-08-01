package connector

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"e2m.local/contracts"
)

const (
	upstreamIntelligenceSourceBindingsFilename = "upstream-intelligence-source-bindings.json"
	upstreamIntelligenceSourceBindingsVersion  = 1
	maxUpstreamIntelligenceSourceBindingsBytes = 1 << 20
)

type UpstreamIntelligenceSourceBinding struct {
	SourceID string `json:"source_id"`
	LocalRef string `json:"local_ref"`
}

type upstreamIntelligenceSourceBindingsPayload struct {
	Version  int                                 `json:"version"`
	Bindings []UpstreamIntelligenceSourceBinding `json:"bindings"`
}

type upstreamIntelligenceSourceBindingsFile struct {
	Version  int                                 `json:"version"`
	Bindings []UpstreamIntelligenceSourceBinding `json:"bindings"`
	Checksum string                              `json:"checksum"`
}

type UpstreamIntelligenceSourceBindingStore struct {
	path string
	mu   *sync.Mutex
}

var upstreamIntelligenceSourceBindingLocks sync.Map

func NewUpstreamIntelligenceSourceBindingStore(dataDir string) *UpstreamIntelligenceSourceBindingStore {
	path := filepath.Join(dataDir, upstreamIntelligenceSourceBindingsFilename)
	value, _ := upstreamIntelligenceSourceBindingLocks.LoadOrStore(path, &sync.Mutex{})
	return &UpstreamIntelligenceSourceBindingStore{path: path, mu: value.(*sync.Mutex)}
}

func (s *UpstreamIntelligenceSourceBindingStore) Bind(sourceID, localRef string) error {
	if s == nil || s.mu == nil || strings.TrimSpace(s.path) == "" ||
		!contracts.IsConnectorUpstreamIntelligenceSourceID(sourceID) || !validUpstreamIntelligenceLocalRef(localRef) {
		return errors.New("invalid upstream intelligence source binding")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bindings, err := s.loadLocked()
	if errors.Is(err, os.ErrNotExist) {
		bindings = []UpstreamIntelligenceSourceBinding{}
	} else if err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.SourceID == sourceID || binding.LocalRef == localRef {
			if binding.SourceID == sourceID && binding.LocalRef == localRef {
				return nil
			}
			return errors.New("upstream intelligence source binding conflict")
		}
	}
	if len(bindings) >= maxUpstreamIntelligenceSourceRecords {
		return errors.New("too many upstream intelligence source bindings")
	}
	bindings = append(bindings, UpstreamIntelligenceSourceBinding{SourceID: sourceID, LocalRef: localRef})
	return s.saveLocked(bindings)
}

func (s *UpstreamIntelligenceSourceBindingStore) Resolve(sourceID string) (string, error) {
	if s == nil || s.mu == nil || !contracts.IsConnectorUpstreamIntelligenceSourceID(sourceID) {
		return "", os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bindings, err := s.loadLocked()
	if err != nil {
		return "", err
	}
	for _, binding := range bindings {
		if binding.SourceID == sourceID {
			return binding.LocalRef, nil
		}
	}
	return "", os.ErrNotExist
}

func (s *UpstreamIntelligenceSourceBindingStore) loadLocked() ([]UpstreamIntelligenceSourceBinding, error) {
	raw, err := readRegularFileNoSymlink(s.path)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxUpstreamIntelligenceSourceBindingsBytes {
		return nil, errors.New("upstream intelligence source bindings exceed size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var stored upstreamIntelligenceSourceBindingsFile
	if err := decoder.Decode(&stored); err != nil {
		return nil, errors.New("decode upstream intelligence source bindings")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("upstream intelligence source bindings must contain one JSON value")
	}
	if stored.Version != upstreamIntelligenceSourceBindingsVersion || len(stored.Bindings) > maxUpstreamIntelligenceSourceRecords {
		return nil, errors.New("upstream intelligence source bindings version or capacity is invalid")
	}
	want, err := upstreamIntelligenceSourceBindingsChecksum(stored.Bindings)
	if err != nil || want != stored.Checksum {
		return nil, errors.New("upstream intelligence source bindings checksum mismatch")
	}
	seenIDs, seenRefs := map[string]struct{}{}, map[string]struct{}{}
	for _, binding := range stored.Bindings {
		if !contracts.IsConnectorUpstreamIntelligenceSourceID(binding.SourceID) || !validUpstreamIntelligenceLocalRef(binding.LocalRef) {
			return nil, errors.New("upstream intelligence source binding is invalid")
		}
		if _, ok := seenIDs[binding.SourceID]; ok {
			return nil, errors.New("duplicate upstream intelligence source id binding")
		}
		if _, ok := seenRefs[binding.LocalRef]; ok {
			return nil, errors.New("duplicate upstream intelligence local ref binding")
		}
		seenIDs[binding.SourceID], seenRefs[binding.LocalRef] = struct{}{}, struct{}{}
	}
	sort.Slice(stored.Bindings, func(i, j int) bool { return stored.Bindings[i].SourceID < stored.Bindings[j].SourceID })
	return stored.Bindings, nil
}

func (s *UpstreamIntelligenceSourceBindingStore) saveLocked(bindings []UpstreamIntelligenceSourceBinding) error {
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].SourceID < bindings[j].SourceID })
	checksum, err := upstreamIntelligenceSourceBindingsChecksum(bindings)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(upstreamIntelligenceSourceBindingsFile{
		Version: upstreamIntelligenceSourceBindingsVersion, Bindings: bindings, Checksum: checksum,
	})
	if err != nil || len(raw)+1 > maxUpstreamIntelligenceSourceBindingsBytes {
		return errors.New("encode upstream intelligence source bindings")
	}
	return atomicWritePrivateFile(s.path, append(raw, '\n'))
}

func upstreamIntelligenceSourceBindingsChecksum(bindings []UpstreamIntelligenceSourceBinding) (string, error) {
	if bindings == nil {
		bindings = []UpstreamIntelligenceSourceBinding{}
	}
	copyBindings := append([]UpstreamIntelligenceSourceBinding(nil), bindings...)
	sort.Slice(copyBindings, func(i, j int) bool { return copyBindings[i].SourceID < copyBindings[j].SourceID })
	raw, err := json.Marshal(upstreamIntelligenceSourceBindingsPayload{
		Version: upstreamIntelligenceSourceBindingsVersion, Bindings: copyBindings,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
