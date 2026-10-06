package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// bindThreadToSecondCredential warms one thread so round-robin hands the
// target thread the second credential. A fresh manager would give a new
// thread the first credential, so a restored binding is observable.
func bindThreadToSecondCredential(t *testing.T, manager *Manager, thread string) string {
	t.Helper()
	first := executeInThread(t, manager, "warmup-"+thread)
	bound := executeInThread(t, manager, thread)
	if bound == first {
		t.Fatalf("setup: thread bound to %s, want the second credential", bound)
	}
	return bound
}

func TestSessionBindingsSurviveManagerRestart(t *testing.T) {
	ctx := context.Background()
	clock := newAffinityTestClock()
	store := NewFileSessionBindingStore(t.TempDir())

	before := newAffinityClockManager(t, clock, time.Hour)
	bound := bindThreadToSecondCredential(t, before, "thread-restart")
	if errSave := before.SaveSessionBindings(ctx, store); errSave != nil {
		t.Fatalf("save bindings: %v", errSave)
	}

	clock.Advance(5 * time.Minute)
	after := newAffinityClockManager(t, clock, time.Hour)
	if _, errRestore := after.RestoreSessionBindings(ctx, store); errRestore != nil {
		t.Fatalf("restore bindings: %v", errRestore)
	}
	if got := executeInThread(t, after, "thread-restart"); got != bound {
		t.Fatalf("after restart: credential = %s, want restored binding on %s", got, bound)
	}

	// The file is consumed at startup: a crash before the next graceful
	// shutdown must not restore these bindings again.
	if restored, errRestore := after.RestoreSessionBindings(ctx, store); errRestore != nil || restored != 0 {
		t.Fatalf("second restore: restored=%d err=%v, want 0 and no error", restored, errRestore)
	}
}

func TestRestoredBindingMovesOffCredentialStillCoolingDown(t *testing.T) {
	ctx := context.Background()
	clock := newAffinityTestClock()
	authDir := t.TempDir()
	bindings := NewFileSessionBindingStore(authDir)
	cooldowns := NewFileCooldownStateStoreWithAuthDir(authDir, authDir)

	before := newAffinityClockManager(t, clock, time.Hour)
	bound := bindThreadToSecondCredential(t, before, "thread-cooling")
	if errSave := before.SaveSessionBindings(ctx, bindings); errSave != nil {
		t.Fatalf("save bindings: %v", errSave)
	}
	// The bound credential was cooling down at shutdown. The manager's
	// availability check reads the wall clock, so the cooldown uses it too.
	errSaveCooldown := cooldowns.Save(ctx, []CooldownStateRecord{{
		Provider:       "codex",
		AuthID:         bound,
		Model:          "affinity-clock-model",
		Status:         string(StatusError),
		NextRetryAfter: time.Now().Add(time.Hour),
		Reason:         "quota",
		Quota:          QuotaState{Exceeded: true, Reason: "quota"},
	}})
	if errSaveCooldown != nil {
		t.Fatalf("save cooldowns: %v", errSaveCooldown)
	}

	after := newAffinityClockManager(t, clock, time.Hour)
	if !after.SwapCooldownStateStore(ctx, cooldowns, false) {
		t.Fatal("swap cooldown store failed")
	}
	if errRestore := after.RestoreCooldownStates(ctx); errRestore != nil {
		t.Fatalf("restore cooldowns: %v", errRestore)
	}
	if restored, errRestore := after.RestoreSessionBindings(ctx, bindings); errRestore != nil || restored == 0 {
		t.Fatalf("restore bindings: restored=%d err=%v, want the binding restored", restored, errRestore)
	}
	if got := executeInThread(t, after, "thread-cooling"); got == bound {
		t.Fatalf("credential = %s, want the thread moved off the credential still cooling down", got)
	}
}

func TestRestoreSessionBindingsWithoutFileRestoresNothing(t *testing.T) {
	manager := newAffinityClockManager(t, newAffinityTestClock(), time.Hour)
	restored, errRestore := manager.RestoreSessionBindings(context.Background(), NewFileSessionBindingStore(t.TempDir()))
	if errRestore != nil || restored != 0 {
		t.Fatalf("restore without file: restored=%d err=%v, want 0 and no error", restored, errRestore)
	}
}

func TestRestoreSessionBindingsFromCorruptFileReportsErrorAndKeepsRouting(t *testing.T) {
	authDir := t.TempDir()
	if errWrite := os.WriteFile(filepath.Join(authDir, SessionBindingsFileName), []byte("{not json"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	manager := newAffinityClockManager(t, newAffinityTestClock(), time.Hour)
	restored, errRestore := manager.RestoreSessionBindings(context.Background(), NewFileSessionBindingStore(authDir))
	if errRestore == nil || restored != 0 {
		t.Fatalf("restore from corrupt file: restored=%d err=%v, want 0 and an error", restored, errRestore)
	}
	first := executeInThread(t, manager, "thread-after-corrupt")
	if got := executeInThread(t, manager, "thread-after-corrupt"); got != first {
		t.Fatalf("routing after corrupt file: credential = %s, want new binding kept on %s", got, first)
	}
}

func TestSessionBindingsSurviveRoutingSelectorRebuild(t *testing.T) {
	clock := newAffinityTestClock()
	manager := newAffinityClockManager(t, clock, time.Hour)
	bound := bindThreadToSecondCredential(t, manager, "thread-reload")

	// A routing hot reload installs a freshly built affinity selector.
	rebuilt := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		NowFunc:  clock.Now,
	})
	t.Cleanup(rebuilt.Stop)
	clock.Advance(10 * time.Minute)
	manager.SetSelector(rebuilt)

	if got := executeInThread(t, manager, "thread-reload"); got != bound {
		t.Fatalf("after selector rebuild: credential = %s, want binding kept on %s", got, bound)
	}
}

func TestSessionBindingsExpiredWhileDownAreDropped(t *testing.T) {
	ctx := context.Background()
	clock := newAffinityTestClock()
	store := NewFileSessionBindingStore(t.TempDir())

	before := newAffinityClockManager(t, clock, time.Hour)
	bindThreadToSecondCredential(t, before, "thread-stale")
	clock.Advance(50 * time.Minute)
	warm := bindThreadToSecondCredential(t, before, "thread-warm")
	if errSave := before.SaveSessionBindings(ctx, store); errSave != nil {
		t.Fatalf("save bindings: %v", errSave)
	}

	// Down for 20 minutes: the stale thread and its warmup (idle 70 minutes)
	// expired; the warm thread and its warmup (idle 20 minutes) did not.
	clock.Advance(20 * time.Minute)
	after := newAffinityClockManager(t, clock, time.Hour)
	restored, errRestore := after.RestoreSessionBindings(ctx, store)
	if errRestore != nil {
		t.Fatalf("restore bindings: %v", errRestore)
	}
	if restored != 2 {
		t.Fatalf("restored bindings = %d, want 2 (warm thread and its warmup)", restored)
	}
	if got := executeInThread(t, after, "thread-warm"); got != warm {
		t.Fatalf("warm thread: credential = %s, want restored binding on %s", got, warm)
	}
}
