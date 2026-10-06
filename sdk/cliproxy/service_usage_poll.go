package cliproxy

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagepoll"
	log "github.com/sirupsen/logrus"
)

// startUsagePoller starts the core usage poller in the background. It polls
// idle credentials once at startup and then on a timer, and records the
// results in the manager's quota readings. It stops when ctx is done or
// stopUsagePoller runs.
func (s *Service) startUsagePoller(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	pollCtx, cancel := context.WithCancel(ctx)
	s.usagePollerMu.Lock()
	if s.usagePollerCancel != nil {
		s.usagePollerCancel()
	}
	s.usagePollerCancel = cancel
	s.usagePollerMu.Unlock()

	poller := usagepoll.New(usagepoll.Config{
		Credentials: s.coreManager,
		Readings:    s.coreManager.QuotaReadings(),
		Fetchers: []usagepoll.Fetcher{
			usagepoll.NewCodexFetcher(s.coreManager, ""),
		},
	})
	go poller.Run(pollCtx, usagepoll.DefaultCheckInterval)
	log.Infof("core usage poller started (check interval=%s)", usagepoll.DefaultCheckInterval)
}

// stopUsagePoller stops the usage poller if it runs.
func (s *Service) stopUsagePoller() {
	if s == nil {
		return
	}
	s.usagePollerMu.Lock()
	cancel := s.usagePollerCancel
	s.usagePollerCancel = nil
	s.usagePollerMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
