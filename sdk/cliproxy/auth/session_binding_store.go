package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
)

// SessionBindingsFileName is the bindings file written to the auth directory on
// graceful shutdown. It does not end in .json, so the auth watcher ignores it.
const SessionBindingsFileName = "session-bindings.state"

// SessionBindingRecord is one persisted binding: a thread's binding keys (the
// first is the primary key, the rest are its aliases), the credential they are
// bound to, and when the binding expires.
type SessionBindingRecord struct {
	AuthID    string    `json:"auth_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Keys      []string  `json:"keys"`
}

// SessionBindingStore persists session-affinity bindings across restarts.
type SessionBindingStore interface {
	Load(context.Context) ([]SessionBindingRecord, error)
	Save(context.Context, []SessionBindingRecord) error
}

type sessionBindingsFile struct {
	Version  int                    `json:"version"`
	SavedAt  time.Time              `json:"saved_at"`
	Bindings []SessionBindingRecord `json:"bindings"`
}

// FileSessionBindingStore stores bindings in one 0600 file in the auth directory.
type FileSessionBindingStore struct {
	mu   sync.Mutex
	path string
}

// NewFileSessionBindingStore creates a store that keeps bindings in
// SessionBindingsFileName inside authDir.
func NewFileSessionBindingStore(authDir string) *FileSessionBindingStore {
	authDir = strings.TrimSpace(authDir)
	if authDir == "" {
		return &FileSessionBindingStore{}
	}
	return &FileSessionBindingStore{path: filepath.Join(authDir, SessionBindingsFileName)}
}

// Path returns the bindings file path.
func (s *FileSessionBindingStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Load reads the bindings file. A missing file is treated as no bindings.
func (s *FileSessionBindingStore) Load(ctx context.Context) ([]SessionBindingRecord, error) {
	if s == nil || s.path == "" {
		return nil, nil
	}
	if ctx != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			return nil, errCtx
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, errRead := os.ReadFile(s.path)
	if errRead != nil {
		if errors.Is(errRead, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session bindings %s: %w", s.path, errRead)
	}
	var envelope sessionBindingsFile
	if errUnmarshal := json.Unmarshal(data, &envelope); errUnmarshal != nil {
		return nil, fmt.Errorf("parse session bindings %s: %w", s.path, errUnmarshal)
	}
	return envelope.Bindings, nil
}

// Save atomically replaces the bindings file with a 0600 file. Saving no
// bindings removes the file.
func (s *FileSessionBindingStore) Save(ctx context.Context, records []SessionBindingRecord) error {
	if s == nil || s.path == "" {
		return nil
	}
	if ctx != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(records) == 0 {
		if errRemove := os.Remove(s.path); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			return fmt.Errorf("remove session bindings %s: %w", s.path, errRemove)
		}
		return nil
	}
	data, errMarshal := json.Marshal(sessionBindingsFile{Version: 1, SavedAt: time.Now().UTC(), Bindings: records})
	if errMarshal != nil {
		return fmt.Errorf("marshal session bindings: %w", errMarshal)
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.path), 0o700); errMkdir != nil {
		return fmt.Errorf("create session bindings directory: %w", errMkdir)
	}
	tmp := s.path + ".tmp"
	file, errCreate := misc.CreateCredentialFile(tmp)
	if errCreate != nil {
		return fmt.Errorf("create session bindings temp file: %w", errCreate)
	}
	_, errWrite := file.Write(data)
	errClose := file.Close()
	if errWrite != nil || errClose != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write session bindings temp file: %w", errors.Join(errWrite, errClose))
	}
	if errRename := os.Rename(tmp, s.path); errRename != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace session bindings file: %w", errRename)
	}
	return nil
}

// sessionBindingHolder is implemented by selectors that hold session bindings.
type sessionBindingHolder interface {
	sessionBindings() []SessionBindingRecord
	restoreSessionBindings([]SessionBindingRecord) int
}

// SaveSessionBindings writes the current selector's unexpired bindings to store.
// A selector without session affinity has no bindings, so the store is cleared.
func (m *Manager) SaveSessionBindings(ctx context.Context, store SessionBindingStore) error {
	if m == nil || store == nil {
		return nil
	}
	var records []SessionBindingRecord
	if holder, ok := m.Selector().(sessionBindingHolder); ok {
		records = holder.sessionBindings()
	}
	return store.Save(ctx, records)
}

// RestoreSessionBindings loads bindings from store into the current selector and
// returns how many were restored. Bindings that expired while the proxy was down,
// or that point to credentials no longer registered, are dropped. The store is
// cleared after loading, so a later crash cannot restore stale bindings.
func (m *Manager) RestoreSessionBindings(ctx context.Context, store SessionBindingStore) (int, error) {
	if m == nil || store == nil {
		return 0, nil
	}
	records, errLoad := store.Load(ctx)
	if errLoad != nil {
		return 0, errLoad
	}
	if len(records) == 0 {
		return 0, nil
	}
	if errClear := store.Save(ctx, nil); errClear != nil {
		logEntryWithRequestID(ctx).Warnf("failed to clear restored session bindings: %v", errClear)
	}
	holder, ok := m.Selector().(sessionBindingHolder)
	if !ok {
		return 0, nil
	}
	known := make([]SessionBindingRecord, 0, len(records))
	m.mu.RLock()
	for _, record := range records {
		if _, exists := m.auths[record.AuthID]; exists {
			known = append(known, record)
		}
	}
	m.mu.RUnlock()
	return holder.restoreSessionBindings(known), nil
}
