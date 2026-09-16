// Package metrics is the one place in the project that knows about
// Prometheus, mirroring how cmd/croupier is the one place that knows about
// concrete infrastructure (Postgres/SQS/Keycloak) — see ARCHITECTURE.md →
// "Observabilidade". Every other package that reports a metric (internal/
// httpapi, internal/sqs, internal/app) does so through a small, consumer-
// defined interface (same pattern already used for repositories/publishers
// throughout this codebase) that *Registry satisfies structurally, without
// any of those packages importing Prometheus themselves.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds every metric this application exposes. A zero Registry is
// not usable — always build one with New, which registers every collector
// against a fresh, private prometheus.Registry (not the global
// DefaultRegisterer), so multiple *Registry values never collide and tests
// never need to worry about global metric state leaking between them.
type Registry struct {
	registry *prometheus.Registry

	httpRequestDuration *prometheus.HistogramVec

	wagerSubmissionsTotal *prometheus.CounterVec

	reconciliationsTotal     *prometheus.CounterVec
	reconciliationDifference prometheus.Histogram

	outboxPublishTotal   *prometheus.CounterVec
	outboxPublishLatency prometheus.Histogram

	pendingReferenceResolutionsTotal *prometheus.CounterVec

	sqsMessagesTotal *prometheus.CounterVec

	dlqDepth *prometheus.GaugeVec
}

func New() *Registry {
	reg := prometheus.NewRegistry()
	factory := promauto.With(reg)

	return &Registry{
		registry: reg,

		httpRequestDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "croupier_http_request_duration_seconds",
			Help:    "HTTP request latency in seconds, by method, route pattern and status.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "pattern", "status"}),

		// outcome: processed | rejected | pending_reference | replay. A
		// "replay" here covers both an ordinary resubmission and the insert-
		// race case WagerSubmitter.Submit retries as a replay (Fase 12) —
		// from outside Submit's boundary the two are the identical
		// observable outcome by design, so they share one label value
		// instead of a separate, redundant "concurrency conflict" metric.
		wagerSubmissionsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "croupier_wager_submissions_total",
			Help: "Wager transaction submissions, by kind and outcome (processed, rejected, pending_reference, replay).",
		}, []string{"kind", "outcome"}),

		reconciliationsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "croupier_wallet_reconciliations_total",
			Help: "Wallet reconciliations run, by whether the stored balance matched the ledger.",
		}, []string{"consistent"}),
		// Not labeled by walletId: that would be unbounded cardinality for a
		// value Prometheus is a poor fit to store per-entity anyway — the
		// distribution here is what answers "do divergences happen and how
		// large are they", the reconciliation HTTP response itself already
		// carries the exact walletId/difference for a specific call.
		reconciliationDifference: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "croupier_wallet_reconciliation_difference_minor_units",
			Help:    "Absolute difference between stored and calculated balance, in currency minor units, per reconciliation run (0 when consistent).",
			Buckets: []float64{0, 1, 10, 100, 1000, 10000, 100000},
		}),

		// outcome: success | retry.
		outboxPublishTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "croupier_outbox_publish_total",
			Help: "Outbox entry publish attempts, by outcome (success, retry).",
		}, []string{"outcome"}),
		outboxPublishLatency: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "croupier_outbox_publish_latency_seconds",
			Help:    "Time from an outbox entry's occurredAt to a successful publish.",
			Buckets: prometheus.DefBuckets,
		}),

		// outcome: resolved | still_pending | expired.
		pendingReferenceResolutionsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "croupier_pending_reference_resolutions_total",
			Help: "PENDING_REFERENCE resolution attempts, by outcome (resolved, still_pending, expired).",
		}, []string{"outcome"}),

		// result: success | error.
		sqsMessagesTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "croupier_sqs_messages_processed_total",
			Help: "SQS messages processed by the consumer, by consumer name and result (success, error).",
		}, []string{"consumer", "result"}),

		dlqDepth: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "croupier_sqs_dlq_depth",
			Help: "Approximate number of messages currently in a dead-letter queue, by queue name.",
		}, []string{"queue"}),
	}
}

// Handler serves the text exposition format for GET /metrics.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

func (r *Registry) ObserveRequest(method, pattern string, status int, duration time.Duration) {
	r.httpRequestDuration.WithLabelValues(method, pattern, statusBucket(status)).Observe(duration.Seconds())
}

// statusBucket keeps the metric's cardinality bounded (a handful of classes
// instead of one series per distinct status code, still enough to tell "200
// vs 4xx vs 5xx" apart at a glance).
func statusBucket(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}

func (r *Registry) ObserveWagerSubmission(kind, outcome string) {
	r.wagerSubmissionsTotal.WithLabelValues(kind, outcome).Inc()
}

func (r *Registry) ObserveReconciliation(consistent bool, differenceMinorUnits int64) {
	label := "true"
	if !consistent {
		label = "false"
	}
	r.reconciliationsTotal.WithLabelValues(label).Inc()
	if differenceMinorUnits < 0 {
		differenceMinorUnits = -differenceMinorUnits
	}
	r.reconciliationDifference.Observe(float64(differenceMinorUnits))
}

func (r *Registry) ObserveOutboxPublish(outcome string, age time.Duration) {
	r.outboxPublishTotal.WithLabelValues(outcome).Inc()
	if outcome == "success" {
		r.outboxPublishLatency.Observe(age.Seconds())
	}
}

func (r *Registry) ObservePendingReferenceResolution(outcome string) {
	r.pendingReferenceResolutionsTotal.WithLabelValues(outcome).Inc()
}

func (r *Registry) ObserveSQSMessage(consumer, result string) {
	r.sqsMessagesTotal.WithLabelValues(consumer, result).Inc()
}

func (r *Registry) SetDLQDepth(queue string, depth float64) {
	r.dlqDepth.WithLabelValues(queue).Set(depth)
}
