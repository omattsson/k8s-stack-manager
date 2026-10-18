package cluster

import (
	"context"
	"testing"
	"time"
)

// TestWorkers_RunStartStopStartAgain checks that each leader-only worker in
// this package can run, stop and run again (one Run per leadership term).
func TestWorkers_RunStartStopStartAgain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		run  func(ctx context.Context)
		name string
	}{
		{name: "health poller", run: NewHealthPoller(HealthPollerConfig{Interval: time.Hour}).Run},
		{name: "quota monitor", run: NewQuotaMonitor(QuotaMonitorConfig{Interval: time.Hour}).Run},
		{name: "secret monitor", run: NewSecretMonitor(SecretMonitorConfig{Interval: time.Hour}).Run},
		{name: "secret refresher", run: NewSecretRefresher(SecretRefresherConfig{Interval: time.Hour}).Run},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for term := 0; term < 3; term++ {
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() {
					defer close(done)
					tt.run(ctx)
				}()
				cancel()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatalf("term %d: Run did not return after cancel", term)
				}
			}
		})
	}
}
