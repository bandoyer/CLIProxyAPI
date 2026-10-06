package quotareading

import (
	"strings"
	"sync"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

type resetLogTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *resetLogTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *resetLogTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func captureResetLogLines(t *testing.T) func() []string {
	t.Helper()
	hook := new(logtest.Hook)
	oldLevel := log.GetLevel()
	savedHooks := make(log.LevelHooks)
	for level, hooks := range log.StandardLogger().Hooks {
		savedHooks[level] = append([]log.Hook(nil), hooks...)
	}
	log.SetLevel(log.InfoLevel)
	log.AddHook(hook)
	t.Cleanup(func() {
		log.SetLevel(oldLevel)
		log.StandardLogger().ReplaceHooks(savedHooks)
	})
	return func() []string {
		var lines []string
		for _, entry := range hook.AllEntries() {
			if strings.HasPrefix(entry.Message, "quota window reset") {
				lines = append(lines, entry.Message)
			}
		}
		return lines
	}
}

func newResetLogTest(t *testing.T) (*Store, *resetLogTestClock, *ResetLog, func() []string) {
	t.Helper()
	lines := captureResetLogLines(t)
	store := NewStore()
	clock := &resetLogTestClock{now: storeTestNow}
	resetLog := NewResetLog(ResetLogConfig{
		Store:    store,
		NowFunc:  clock.Now,
		Provider: func(string) string { return "claude" },
	})
	return store, clock, resetLog, lines
}

func TestResetLogLogsOneLineWhenTheClockPassesAWindowsResetTime(t *testing.T) {
	store, clock, resetLog, lines := newResetLogTest(t)
	store.Record("claude-a", Reading{
		Window: ClaudeSevenDayWindow, Kind: KindRanking, Length: 7 * 24 * time.Hour,
		ShareLeft: 0.42, ResetAt: storeTestNow.Add(2 * time.Hour), LearnedAt: storeTestNow, Source: SourceHeader,
	})

	resetLog.Check()
	if got := lines(); len(got) != 0 {
		t.Fatalf("lines before the reset time = %q, want none", got)
	}

	clock.Advance(2*time.Hour + time.Second)
	resetLog.Check()
	resetLog.Check()
	clock.Advance(time.Hour)
	resetLog.Check()

	want := "quota window reset | credential=claude-a provider=claude window=seven_day share_left=0.4200" +
		" reset_at=2026-10-06T14:00:00Z kind=ranking learned_at=2026-10-06T12:00:00Z"
	got := lines()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("lines = %q, want exactly one %q", got, want)
	}
}

func TestResetLogLogsNothingForAWindowWithoutAPendingReset(t *testing.T) {
	store, clock, resetLog, lines := newResetLogTest(t)
	// A credit balance has no reset time; the credential has no seven-day reading.
	store.Record("claude-a", Reading{Window: "extra_usage", Kind: KindCredit, ShareLeft: 0.5, LearnedAt: storeTestNow, Source: SourcePoll})

	clock.Advance(8 * 24 * time.Hour)
	resetLog.Check()

	if got := lines(); len(got) != 0 {
		t.Fatalf("lines = %q, want none for windows without a reading or reset time", got)
	}
}

func TestResetLogLogsEachResetOfAWindowOnce(t *testing.T) {
	store, clock, resetLog, lines := newResetLogTest(t)
	fiveHour := func(shareLeft float64, learnedAt, resetAt time.Time) Reading {
		return Reading{Window: ClaudeFiveHourWindow, Kind: KindGating, Length: 5 * time.Hour, ShareLeft: shareLeft, ResetAt: resetAt, LearnedAt: learnedAt, Source: SourceHeader}
	}
	store.Record("claude-a", fiveHour(0.8, storeTestNow, storeTestNow.Add(time.Hour)))
	clock.Advance(time.Hour)
	resetLog.Check()

	// The next response brings the next window; it resets five hours later.
	store.Record("claude-a", fiveHour(0.25, clock.Now(), storeTestNow.Add(6*time.Hour)))
	resetLog.Check()
	clock.Advance(5 * time.Hour)
	resetLog.Check()
	resetLog.Check()

	want := []string{
		"quota window reset | credential=claude-a provider=claude window=five_hour share_left=0.8000" +
			" reset_at=2026-10-06T13:00:00Z kind=gating learned_at=2026-10-06T12:00:00Z",
		"quota window reset | credential=claude-a provider=claude window=five_hour share_left=0.2500" +
			" reset_at=2026-10-06T18:00:00Z kind=gating learned_at=2026-10-06T13:00:00Z",
	}
	got := lines()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}
