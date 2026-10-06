package quotareading

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// ResetLogMessagePrefix starts every window-reset log line.
	ResetLogMessagePrefix = "quota window reset"
	// DefaultResetLogInterval is how often Run checks for passed reset times.
	// A line can come up to one interval after the reset; its reset_at field
	// holds the exact reset time.
	DefaultResetLogInterval = time.Minute
)

// ResetLogConfig configures a ResetLog.
type ResetLogConfig struct {
	// Store holds the readings to watch.
	Store *Store
	// NowFunc returns the current time. Nil means time.Now.
	NowFunc func() time.Time
	// Provider returns the provider of a credential, for the log line. Nil or
	// an empty result logs "-".
	Provider func(credentialID string) string
}

// ResetLog logs one line when a quota window in a reading reaches its reset
// time, with the share left from the last reading. The line is the source for
// wasted-quota alerts and the routing report.
type ResetLog struct {
	store    *Store
	now      func() time.Time
	provider func(credentialID string) string

	mu sync.Mutex
	// logged holds the last reset time logged per credential and window.
	logged map[resetLogKey]time.Time
}

type resetLogKey struct {
	credentialID string
	window       string
}

// NewResetLog returns a ResetLog over cfg.Store.
func NewResetLog(cfg ResetLogConfig) *ResetLog {
	now := cfg.NowFunc
	if now == nil {
		now = time.Now
	}
	return &ResetLog{
		store:    cfg.Store,
		now:      now,
		provider: cfg.Provider,
		logged:   make(map[resetLogKey]time.Time),
	}
}

// Check logs every window whose reset time has been reached since it was
// last logged. Each reset time of a credential's window is logged once.
func (l *ResetLog) Check() {
	if l == nil || l.store == nil {
		return
	}
	now := l.now()
	snapshot := l.store.Snapshot()
	credentialIDs := make([]string, 0, len(snapshot))
	for credentialID := range snapshot {
		credentialIDs = append(credentialIDs, credentialID)
	}
	sort.Strings(credentialIDs)
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, credentialID := range credentialIDs {
		for _, reading := range snapshot[credentialID] {
			if reading.ResetAt.IsZero() || now.Before(reading.ResetAt) {
				continue
			}
			key := resetLogKey{credentialID: credentialID, window: reading.Window}
			if last, ok := l.logged[key]; ok && !reading.ResetAt.After(last) {
				continue
			}
			l.logged[key] = reading.ResetAt
			log.Infof("%s | credential=%s provider=%s window=%s share_left=%.4f reset_at=%s kind=%s learned_at=%s",
				ResetLogMessagePrefix,
				credentialID,
				l.providerOf(credentialID),
				reading.Window,
				reading.ShareLeft,
				reading.ResetAt.UTC().Format(time.RFC3339),
				reading.Kind,
				reading.LearnedAt.UTC().Format(time.RFC3339),
			)
		}
	}
}

// Run calls Check every interval until ctx is done. A non-positive interval
// means DefaultResetLogInterval.
func (l *ResetLog) Run(ctx context.Context, interval time.Duration) {
	if l == nil {
		return
	}
	if interval <= 0 {
		interval = DefaultResetLogInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.Check()
		}
	}
}

func (l *ResetLog) providerOf(credentialID string) string {
	if l.provider == nil {
		return "-"
	}
	provider := strings.TrimSpace(l.provider(credentialID))
	if provider == "" {
		return "-"
	}
	return provider
}
