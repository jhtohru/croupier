package metrics

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryObserveWagerSubmission(t *testing.T) {
	r := New()
	r.ObserveWagerSubmission("BET", "processed")
	r.ObserveWagerSubmission("BET", "processed")
	r.ObserveWagerSubmission("REFUND", "pending_reference")

	assert.Equal(t, float64(2), testutil.ToFloat64(r.wagerSubmissionsTotal.WithLabelValues("BET", "processed")))
	assert.Equal(t, float64(1), testutil.ToFloat64(r.wagerSubmissionsTotal.WithLabelValues("REFUND", "pending_reference")))
}

func TestRegistryObserveReconciliation(t *testing.T) {
	r := New()
	r.ObserveReconciliation(true, 0)
	r.ObserveReconciliation(false, -500) // sign shouldn't matter — histogram observes the magnitude

	assert.Equal(t, float64(1), testutil.ToFloat64(r.reconciliationsTotal.WithLabelValues("true")))
	assert.Equal(t, float64(1), testutil.ToFloat64(r.reconciliationsTotal.WithLabelValues("false")))

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	assert.Contains(t, rec.Body.String(), "croupier_wallet_reconciliation_difference_minor_units_count 2")
}

func TestRegistryObserveOutboxPublish(t *testing.T) {
	r := New()
	r.ObserveOutboxPublish("success", 250*time.Millisecond)
	r.ObserveOutboxPublish("retry", 0)

	assert.Equal(t, float64(1), testutil.ToFloat64(r.outboxPublishTotal.WithLabelValues("success")))
	assert.Equal(t, float64(1), testutil.ToFloat64(r.outboxPublishTotal.WithLabelValues("retry")))

	// Only the success outcome should feed the latency histogram — a retry
	// hasn't actually published anything yet, so it has no latency to
	// report. testutil.ToFloat64/CollectAndCount don't expose a histogram's
	// _count directly, so read it from the exposition text itself.
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	assert.Contains(t, rec.Body.String(), "croupier_outbox_publish_latency_seconds_count 1")
}

func TestRegistrySetDLQDepth(t *testing.T) {
	r := New()
	r.SetDLQDepth("wager-transactions-dlq.fifo", 3)

	assert.Equal(t, float64(3), testutil.ToFloat64(r.dlqDepth.WithLabelValues("wager-transactions-dlq.fifo")))
}

// TestRegistryHandlerServesExpositionFormat is the one point of contact with
// the outside world this package has (GET /metrics) — everything else here
// tests the collectors directly, this confirms they're actually wired into
// the registry Handler serves, not just constructed and forgotten.
func TestRegistryHandlerServesExpositionFormat(t *testing.T) {
	r := New()
	r.ObserveWagerSubmission("BET", "processed")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	r.Handler().ServeHTTP(rec, req)

	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `croupier_wager_submissions_total{kind="BET",outcome="processed"} 1`)
}

func TestStatusBucket(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{200, "2xx"},
		{201, "2xx"},
		{301, "3xx"},
		{400, "4xx"},
		{404, "4xx"},
		{500, "5xx"},
		{503, "5xx"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, statusBucket(tt.status))
	}
}
