package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newRenewalDayTestHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	authDir := t.TempDir()
	fileName := "claude-dan.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"claude","email":"dan@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("write credential file: %v", errWrite)
	}
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:         fileName,
		FileName:   fileName,
		Provider:   "claude",
		Attributes: map[string]string{"path": filePath},
		Metadata:   map[string]any{"type": "claude", "email": "dan@example.com"},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("register credential: %v", errRegister)
	}
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager), filePath
}

func patchCredentialFields(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/fields", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchAuthFileFields(ctx)
	return rec
}

func listedCredential(t *testing.T, h *Handler, name string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil)
	h.ListAuthFiles(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("decode list: %v", errUnmarshal)
	}
	for _, file := range payload.Files {
		if file["name"] == name {
			return file
		}
	}
	t.Fatalf("credential %s not in list: %s", name, rec.Body.String())
	return nil
}

func readCredentialFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read credential file: %v", errRead)
	}
	var data map[string]any
	if errUnmarshal := json.Unmarshal(raw, &data); errUnmarshal != nil {
		t.Fatalf("decode credential file: %v", errUnmarshal)
	}
	return data
}

func TestPatchAuthFileFields_RenewalDayPersistsAndCredentialListReturnsIt(t *testing.T) {
	h, filePath := newRenewalDayTestHandler(t)

	rec := patchCredentialFields(t, h, `{"name":"claude-dan.json","renewal_day":23}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if got := readCredentialFile(t, filePath)["renewal_day"]; got != float64(23) {
		t.Fatalf("file renewal_day = %#v, want 23", got)
	}
	if got := listedCredential(t, h, "claude-dan.json")["renewal_day"]; got != float64(23) {
		t.Fatalf("listed renewal_day = %#v, want 23", got)
	}
}

func TestPatchAuthFileFields_NullRenewalDayClearsIt(t *testing.T) {
	h, filePath := newRenewalDayTestHandler(t)
	if rec := patchCredentialFields(t, h, `{"name":"claude-dan.json","renewal_day":5}`); rec.Code != http.StatusOK {
		t.Fatalf("set status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec := patchCredentialFields(t, h, `{"name":"claude-dan.json","renewal_day":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if _, saved := readCredentialFile(t, filePath)["renewal_day"]; saved {
		t.Fatalf("renewal_day still in the credential file after clearing")
	}
	if got, listed := listedCredential(t, h, "claude-dan.json")["renewal_day"]; listed {
		t.Fatalf("listed renewal_day = %#v after clearing, want absent", got)
	}
}

func TestListAuthFilesFromDisk_ReturnsRenewalDay(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	authDir := t.TempDir()
	if errWrite := os.WriteFile(filepath.Join(authDir, "claude-dan.json"), []byte(`{"type":"claude","renewal_day":23}`), 0o600); errWrite != nil {
		t.Fatalf("write credential file: %v", errWrite)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	if got := listedCredential(t, h, "claude-dan.json")["renewal_day"]; got != float64(23) {
		t.Fatalf("listed renewal_day = %#v, want 23", got)
	}
}

func TestPatchAuthFileFields_RejectsRenewalDayOutsideOneToThirtyOne(t *testing.T) {
	cases := map[string]string{
		"zero":        `{"name":"claude-dan.json","renewal_day":0}`,
		"thirty-two":  `{"name":"claude-dan.json","renewal_day":32}`,
		"negative":    `{"name":"claude-dan.json","renewal_day":-1}`,
		"fraction":    `{"name":"claude-dan.json","renewal_day":1.5}`,
		"string":      `{"name":"claude-dan.json","renewal_day":"15"}`,
		"bool":        `{"name":"claude-dan.json","renewal_day":true}`,
		"nested":      `{"name":"claude-dan.json","renewal_day.day":15}`,
		"huge number": `{"name":"claude-dan.json","renewal_day":9223372036854775808}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h, filePath := newRenewalDayTestHandler(t)

			rec := patchCredentialFields(t, h, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			var payload map[string]string
			if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
				t.Fatalf("decode error body: %v", errUnmarshal)
			}
			if want := "renewal_day must be an integer from 1 to 31, or null to clear it"; payload["error"] != want {
				t.Fatalf("error = %q, want %q", payload["error"], want)
			}
			if _, saved := readCredentialFile(t, filePath)["renewal_day"]; saved {
				t.Fatalf("rejected renewal_day was written to the credential file")
			}
		})
	}
}
