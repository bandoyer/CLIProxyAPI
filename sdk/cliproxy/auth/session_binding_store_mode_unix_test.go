//go:build unix

package auth

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSavedSessionBindingsFileIsMode0600InAuthDir(t *testing.T) {
	oldUmask := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(oldUmask) })

	ctx := context.Background()
	authDir := t.TempDir()
	manager := newAffinityClockManager(t, newAffinityTestClock(), time.Hour)
	executeInThread(t, manager, "thread-mode")
	if errSave := manager.SaveSessionBindings(ctx, NewFileSessionBindingStore(authDir)); errSave != nil {
		t.Fatalf("save bindings: %v", errSave)
	}

	info, errStat := os.Stat(filepath.Join(authDir, SessionBindingsFileName))
	if errStat != nil {
		t.Fatalf("bindings file in auth directory: %v", errStat)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("bindings file mode = %04o, want 0600", got)
	}
}
