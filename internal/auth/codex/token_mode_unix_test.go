//go:build unix

package codex

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSaveTokenToFile_CredentialFileMode0600(t *testing.T) {
	oldUmask := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(oldUmask) })

	authFilePath := filepath.Join(t.TempDir(), "codex-user@example.com.json")
	storage := &CodexTokenStorage{AccessToken: "access", RefreshToken: "refresh"}

	if errSave := storage.SaveTokenToFile(authFilePath); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}
	assertMode0600(t, authFilePath)

	// A token refresh rewrites the file; a file left readable by an older login is tightened.
	if errChmod := os.Chmod(authFilePath, 0o644); errChmod != nil {
		t.Fatalf("os.Chmod error = %v", errChmod)
	}
	storage.AccessToken = "refreshed-access"
	if errSave := storage.SaveTokenToFile(authFilePath); errSave != nil {
		t.Fatalf("SaveTokenToFile() rewrite error = %v", errSave)
	}
	assertMode0600(t, authFilePath)
}

func assertMode0600(t *testing.T, path string) {
	t.Helper()
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("os.Stat error = %v", errStat)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential file mode = %04o, want 0600", got)
	}
}
