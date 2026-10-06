package misc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Separator used to visually group related log lines.
var credentialSeparator = strings.Repeat("-", 67)

// LogSavingCredentials emits a consistent log message when persisting auth material.
func LogSavingCredentials(path string) {
	if path == "" {
		return
	}
	// Use filepath.Clean so logs remain stable even if callers pass redundant separators.
	fmt.Printf("Saving credentials to %s\n", filepath.Clean(path))
}

// CreateCredentialFile opens path for writing credential data with mode 0600,
// creating or truncating it. The mode is applied explicitly so it holds whatever
// the umask is, and an existing file left readable by others is tightened.
func CreateCredentialFile(path string) (*os.File, error) {
	f, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if errOpen != nil {
		return nil, errOpen
	}
	if errChmod := f.Chmod(0o600); errChmod != nil {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("credential file: close after chmod failure: %v", errClose)
		}
		return nil, fmt.Errorf("set credential file mode: %w", errChmod)
	}
	return f, nil
}

// LogCredentialSeparator adds a visual separator to group auth/key processing logs.
func LogCredentialSeparator() {
	log.Debug(credentialSeparator)
}

// MergeMetadata serializes the source struct into a map and merges the provided metadata into it.
func MergeMetadata(source any, metadata map[string]any) (map[string]any, error) {
	var data map[string]any

	// Fast path: if source is already a map, just copy it to avoid mutation of original
	if srcMap, ok := source.(map[string]any); ok {
		data = make(map[string]any, len(srcMap)+len(metadata))
		for k, v := range srcMap {
			data[k] = v
		}
	} else if source != nil {
		// Slow path: marshal to JSON and back to map to respect JSON tags
		temp, errMarshal := json.Marshal(source)
		if errMarshal != nil {
			return nil, fmt.Errorf("failed to marshal source: %w", errMarshal)
		}
		if errUnmarshal := json.Unmarshal(temp, &data); errUnmarshal != nil {
			return nil, fmt.Errorf("failed to unmarshal to map: %w", errUnmarshal)
		}
	}

	// Merge extra metadata
	if metadata != nil {
		if data == nil {
			data = make(map[string]any)
		}
		for k, v := range metadata {
			data[k] = v
		}
	}

	return data, nil
}
