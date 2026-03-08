// Package metrics registers GopherGuard-specific Prometheus metrics that
// expose operator health and healing activity. All metrics are registered
// once at startup via the init() function and are safe for concurrent use.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// ReconcileDuration tracks how long each reconciliation loop takes.
	ReconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "gopherguard",
			Name:      "reconcile_duration_seconds",
			Help:      "Duration of AegisWatch reconcile loops in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"result"}, // "success" | "error"
	)

	// HealingAttempts counts healing PR attempts, labelled by outcome.
	HealingAttempts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "gopherguard",
			Name:      "healing_attempts_total",
			Help:      "Total number of healing PR creation attempts.",
		},
		[]string{"deployment", "namespace", "result"}, // result: "created" | "skipped" | "error"
	)

	// LLMRequestDuration tracks LLM call latency per provider.
	LLMRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "gopherguard",
			Name:      "llm_request_duration_seconds",
			Help:      "Duration of LLM diagnosis requests in seconds.",
			Buckets:   []float64{0.5, 1, 2, 5, 10, 30, 60, 120},
		},
		[]string{"provider", "result"}, // result: "success" | "error"
	)

	// AnomaliesDetected counts detected anomalies by type.
	AnomaliesDetected = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "gopherguard",
			Name:      "anomalies_detected_total",
			Help:      "Total number of anomalies detected across watched Deployments.",
		},
		[]string{"namespace", "reason"}, // reason: CrashLoopBackOff | OOMKilled | RestartThreshold | UnavailableReplicas
	)

	// PRsCreated counts successfully opened healing PRs.
	PRsCreated = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "gopherguard",
			Name:      "prs_created_total",
			Help:      "Total number of healing pull requests opened.",
		},
		[]string{"deployment", "namespace"},
	)

	// WatchedDeployments is a gauge of how many Deployments are currently watched.
	WatchedDeployments = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "gopherguard",
			Name:      "watched_deployments",
			Help:      "Number of Deployments currently being watched by AegisWatch CRs.",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(
		ReconcileDuration,
		HealingAttempts,
		LLMRequestDuration,
		AnomaliesDetected,
		PRsCreated,
		WatchedDeployments,
	)
}
