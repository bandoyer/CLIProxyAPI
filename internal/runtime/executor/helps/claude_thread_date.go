package helps

import (
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// defaultClaudeThreadDateTTL matches the default binding lifetime
// (routing.session-affinity-ttl).
const defaultClaudeThreadDateTTL = time.Hour

// ClaudeThreadDates remembers, per thread, the instant at which cloaking first
// injected the date block, so later requests in the thread repeat that date and
// messages[0] does not change at midnight. Entries live in memory and are
// forgotten once a thread has been idle for longer than the binding lifetime.
type ClaudeThreadDates struct {
	mu        sync.Mutex
	entries   map[string]claudeThreadDateEntry
	lastSweep time.Time
}

type claudeThreadDateEntry struct {
	pinned   time.Time
	lastSeen time.Time
}

// NewClaudeThreadDates returns an empty per-thread date store.
func NewClaudeThreadDates() *ClaudeThreadDates {
	return &ClaudeThreadDates{entries: make(map[string]claudeThreadDateEntry)}
}

// Pin returns the instant whose calendar date the thread's date block must
// carry. The first call for a thread records now; later calls within ttl of
// the thread's previous request return the recorded instant and slide its
// lifetime. An empty thread ID is never pinned and always gets now.
func (d *ClaudeThreadDates) Pin(threadID string, now time.Time, ttl time.Duration) time.Time {
	threadID = strings.TrimSpace(threadID)
	if d == nil || threadID == "" {
		return now
	}
	if ttl <= 0 {
		ttl = defaultClaudeThreadDateTTL
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.entries == nil {
		d.entries = make(map[string]claudeThreadDateEntry)
	}
	if now.Sub(d.lastSweep) >= ttl {
		for id, entry := range d.entries {
			if now.Sub(entry.lastSeen) > ttl {
				delete(d.entries, id)
			}
		}
		d.lastSweep = now
	}

	entry, ok := d.entries[threadID]
	if !ok || now.Sub(entry.lastSeen) > ttl {
		entry = claudeThreadDateEntry{pinned: now}
	}
	entry.lastSeen = now
	d.entries[threadID] = entry
	return entry.pinned
}

// ClaudeThreadDateTTL returns the binding lifetime that per-thread dates slide
// with: routing.session-affinity-ttl, or 1h when unset or invalid.
func ClaudeThreadDateTTL(cfg *config.Config) time.Duration {
	if cfg == nil {
		return defaultClaudeThreadDateTTL
	}
	raw := strings.TrimSpace(cfg.Routing.SessionAffinityTTL)
	if raw == "" {
		return defaultClaudeThreadDateTTL
	}
	parsed, errParse := time.ParseDuration(raw)
	if errParse != nil || parsed <= 0 {
		return defaultClaudeThreadDateTTL
	}
	if parsed < time.Second {
		parsed = time.Second
	}
	return parsed
}
