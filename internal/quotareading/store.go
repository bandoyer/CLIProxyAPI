package quotareading

import (
	"sort"
	"strings"
	"sync"
)

// Store holds quota readings in memory, per credential and quota window.
// Readings are not persisted; a restart starts empty. Store is safe for
// concurrent use.
type Store struct {
	mu       sync.RWMutex
	readings map[string]map[string]Reading
	observer Observer
}

// Observer is told which readings a Record call accepted. It runs after the
// store is unlocked, on the caller's goroutine, so it may read the store.
type Observer func(credentialID string, accepted []Reading)

// SetObserver sets the function that every Record call reports its accepted
// readings to. The auth manager uses it to mark a credential with an exhausted
// window quota-exceeded until the window's reset time. Nil removes it.
func (s *Store) SetObserver(observer Observer) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observer = observer
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{readings: make(map[string]map[string]Reading)}
}

// Record merges readings for one credential and returns the readings it
// accepted. A reading replaces the stored reading of the same window when it
// is not older (by LearnedAt), whatever its source. Windows that the new
// readings do not mention are kept, so a window that only polling returns
// stays until a newer reading of that window arrives. Readings without a
// window name, or calls without a credential ID, are ignored. The observer,
// if one is set, is told about the accepted readings.
func (s *Store) Record(credentialID string, readings ...Reading) []Reading {
	credentialID = strings.TrimSpace(credentialID)
	if s == nil || credentialID == "" || len(readings) == 0 {
		return nil
	}
	accepted, observer := s.record(credentialID, readings)
	if observer != nil && len(accepted) > 0 {
		observer(credentialID, append([]Reading(nil), accepted...))
	}
	return accepted
}

func (s *Store) record(credentialID string, readings []Reading) ([]Reading, Observer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var accepted []Reading
	for _, reading := range readings {
		reading.Window = strings.TrimSpace(reading.Window)
		if reading.Window == "" {
			continue
		}
		windows := s.readings[credentialID]
		if windows == nil {
			windows = make(map[string]Reading)
			s.readings[credentialID] = windows
		}
		if stored, ok := windows[reading.Window]; ok && reading.LearnedAt.Before(stored.LearnedAt) {
			continue
		}
		windows[reading.Window] = reading
		accepted = append(accepted, reading)
	}
	return accepted, s.observer
}

// Readings returns the credential's readings that count for a request on
// model, sorted by window name. Per-model windows are included only when they
// apply to model; an empty model excludes them.
func (s *Store) Readings(credentialID, model string) []Reading {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	windows := s.readings[strings.TrimSpace(credentialID)]
	if len(windows) == 0 {
		return nil
	}
	out := make([]Reading, 0, len(windows))
	for _, reading := range windows {
		if reading.AppliesToModel(model) {
			out = append(out, reading)
		}
	}
	sortReadings(out)
	return out
}

// Snapshot returns a copy of every stored reading, keyed by credential ID,
// each credential's readings sorted by window name. Per-model windows are
// included.
func (s *Store) Snapshot() map[string][]Reading {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][]Reading, len(s.readings))
	for credentialID, windows := range s.readings {
		if len(windows) == 0 {
			continue
		}
		list := make([]Reading, 0, len(windows))
		for _, reading := range windows {
			list = append(list, reading)
		}
		sortReadings(list)
		out[credentialID] = list
	}
	return out
}

func sortReadings(readings []Reading) {
	sort.Slice(readings, func(i, j int) bool { return readings[i].Window < readings[j].Window })
}
