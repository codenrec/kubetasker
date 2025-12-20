package controller

import "github.com/prometheus/client_golang/prometheus"

// KtasksProcessed counts how many Ktasks the controller has reconciled
var KtasksProcessed = prometheus.NewCounter(
	prometheus.CounterOpts{
		Name: "ktasks_processed_total",
		Help: "Total number of Ktasks processed by the controller",
	},
)

// Register metrics
func RegisterMetrics() {
	prometheus.MustRegister(KtasksProcessed)
}
