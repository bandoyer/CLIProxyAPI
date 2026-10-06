//go:build unix

package vertex

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSaveTokenToFile_CredentialFileMode0600(t *testing.T) {
	oldUmask := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(oldUmask) })

	authFilePath := filepath.Join(t.TempDir(), "vertex-test.json")
	storage := &VertexCredentialStorage{
		ServiceAccount: map[string]any{"type": "service_account", "private_key": "secret"},
		ProjectID:      "project",
		Email:          "sa@project.iam.gserviceaccount.com",
	}

	if errSave := storage.SaveTokenToFile(authFilePath); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}
	assertMode0600(t, authFilePath)

	// Re-importing rewrites the file; a file left readable by an older import is tightened.
	if errChmod := os.Chmod(authFilePath, 0o644); errChmod != nil {
		t.Fatalf("os.Chmod error = %v", errChmod)
	}
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
