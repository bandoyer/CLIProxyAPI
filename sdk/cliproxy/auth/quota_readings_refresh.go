package auth

import "github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"

// refreshQuotaReadings returns the quota readings in a credential refresh
// result. Devin's refresh fetches usage and stores it in Quota.Signals; other
// providers' refreshes carry none. A result without an observation time
// carries no new usage, so it gives no readings.
func refreshQuotaReadings(refreshed *Auth) []quotareading.Reading {
	if refreshed == nil || refreshed.Quota.ObservedAt.IsZero() {
		return nil
	}
	return quotareading.FromRefreshSignals(refreshed.Provider, refreshed.Quota.Signals, refreshed.Quota.ObservedAt)
}
