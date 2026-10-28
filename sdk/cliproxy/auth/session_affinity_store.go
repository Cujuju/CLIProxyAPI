package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SessionAffinityStateFile is the bindings file in the auth directory. It is not .json,
// so the auth watcher never treats it as a credential.
const SessionAffinityStateFile = "session-affinity.state"

const sessionAffinityStateVersion = 1

// SessionBindings returns the selector's live session bindings.
func (s *SessionAffinitySelector) SessionBindings() []SessionBindingRecord {
	if s == nil {
		return nil
	}
	return s.cache.Snapshot()
}

// RestoreSessionBindings adds persisted bindings and reports how many were restored.
func (s *SessionAffinitySelector) RestoreSessionBindings(records []SessionBindingRecord) int {
	if s == nil {
		return 0
	}
	return s.cache.Restore(records)
}

type sessionAffinityStateEnvelope struct {
	Version  int                    `json:"version"`
	SavedAt  time.Time              `json:"saved_at"`
	Bindings []SessionBindingRecord `json:"bindings"`
}

// FileSessionAffinityStore keeps session bindings in one file, replaced atomically.
type FileSessionAffinityStore struct {
	mu   sync.Mutex
	path string
}

// NewFileSessionAffinityStore stores bindings at path.
func NewFileSessionAffinityStore(path string) *FileSessionAffinityStore {
	return &FileSessionAffinityStore{path: path}
}

// Path returns the bindings file location.
func (s *FileSessionAffinityStore) Path() string { return s.path }

// Load reads the saved bindings. A missing file is empty state.
func (s *FileSessionAffinityStore) Load() ([]SessionBindingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, errRead := os.ReadFile(s.path)
	if errors.Is(errRead, os.ErrNotExist) {
		return nil, nil
	}
	if errRead != nil {
		return nil, fmt.Errorf("read session affinity state: %w", errRead)
	}
	var envelope sessionAffinityStateEnvelope
	if errParse := json.Unmarshal(data, &envelope); errParse != nil {
		return nil, fmt.Errorf("parse session affinity state: %w", errParse)
	}
	if envelope.Version != sessionAffinityStateVersion {
		return nil, nil
	}
	return envelope.Bindings, nil
}

// Save replaces the file with bindings via a temp file and rename.
func (s *FileSessionAffinityStore) Save(bindings []SessionBindingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, errMarshal := json.Marshal(sessionAffinityStateEnvelope{
		Version: sessionAffinityStateVersion, SavedAt: time.Now().UTC(), Bindings: bindings,
	})
	if errMarshal != nil {
		return fmt.Errorf("encode session affinity state: %w", errMarshal)
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.path), 0o700); errMkdir != nil {
		return fmt.Errorf("create session affinity state directory: %w", errMkdir)
	}
	tmp := s.path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0o600); errWrite != nil {
		return fmt.Errorf("write session affinity state: %w", errWrite)
	}
	if errRename := os.Rename(tmp, s.path); errRename != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace session affinity state: %w", errRename)
	}
	return nil
}
