package auth

import (
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
)

// NewQuotaResetLog returns a window-reset log over the manager's quota
// readings. It names each credential's provider from the manager's auths.
// A nil nowFunc means time.Now.
func (m *Manager) NewQuotaResetLog(nowFunc func() time.Time) *quotareading.ResetLog {
	return quotareading.NewResetLog(quotareading.ResetLogConfig{
		Store:   m.QuotaReadings(),
		NowFunc: nowFunc,
		Provider: func(credentialID string) string {
			if auth, ok := m.GetByID(credentialID); ok && auth != nil {
				return auth.Provider
			}
			return ""
		},
	})
}
