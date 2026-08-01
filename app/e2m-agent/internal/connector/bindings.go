package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxLocalBindings = 10000

// LocalBindingStore keeps upstream credentials and proxy values entirely on
// the Connector host. Core tasks carry only the map key.
type LocalBindingStore struct {
	path string
	mu   *sync.Mutex
}

var localBindingStoreLocks sync.Map

func localBindingStoreLock(path string) *sync.Mutex {
	value, _ := localBindingStoreLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func NewLocalBindingStore(dataDir string) *LocalBindingStore {
	path := filepath.Join(dataDir, "gateway-bindings.json")
	return &LocalBindingStore{path: path, mu: localBindingStoreLock(path)}
}

func (s *LocalBindingStore) ResolveBinding(_ context.Context, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || s == nil {
		return "", os.ErrNotExist
	}
	raw, err := readRegularFileNoSymlink(s.path)
	if err != nil {
		return "", err
	}
	var bindings map[string]string
	if err := json.Unmarshal(raw, &bindings); err != nil {
		return "", fmt.Errorf("decode local bindings: %w", err)
	}
	value := strings.TrimSpace(bindings[id])
	if value == "" {
		return "", os.ErrNotExist
	}
	return value, nil
}

func (s *LocalBindingStore) Save(bindings map[string]string) error {
	return s.save(bindings, true)
}

func (s *LocalBindingStore) save(bindings map[string]string, lock bool) error {
	if s == nil {
		return errors.New("local binding store is not configured")
	}
	if lock && s.mu != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	if len(bindings) > maxLocalBindings {
		return errors.New("too many local bindings")
	}
	clean := make(map[string]string, len(bindings))
	for id, value := range bindings {
		id, value = strings.TrimSpace(id), strings.TrimSpace(value)
		if id == "" || value == "" || len(id) > 128 {
			return errors.New("binding ids and values must be non-empty")
		}
		clean[id] = value
	}
	raw, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return err
	}
	return atomicWritePrivateFile(s.path, append(raw, '\n'))
}

// Merge replaces only submitted IDs. This prevents adding one upstream key
// from silently deleting every other binding on the Connector.
func (s *LocalBindingStore) Merge(bindings map[string]string) error {
	if s == nil {
		return errors.New("local binding store is not configured")
	}
	if s.mu != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	current := map[string]string{}
	if raw, err := readRegularFileNoSymlink(s.path); err == nil {
		if err := json.Unmarshal(raw, &current); err != nil {
			return fmt.Errorf("decode local bindings: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for id, value := range bindings {
		current[id] = value
	}
	return s.save(current, false)
}
