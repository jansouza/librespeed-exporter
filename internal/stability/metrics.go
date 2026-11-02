package stability

import "github.com/prometheus/client_golang/prometheus"

// Buckets from 0.5ms doubling up to ~4s, which covers LAN to satellite
// latencies below the default 5s timeout.
var latencyBuckets = prometheus.ExponentialBuckets(0.0005, 2, 14)

type metrics struct {
	latency  *prometheus.HistogramVec
	jitter   *prometheus.HistogramVec
	requests *prometheus.CounterVec
}

func newMetrics() *metrics {
	histogram := func(name, help string) *prometheus.HistogramVec {
		return prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace:                      "librespeed",
			Subsystem:                      "stability",
			Name:                           name,
			Help:                           help,
			Buckets:                        latencyBuckets,
			NativeHistogramBucketFactor:    1.1,
			NativeHistogramMaxBucketNumber: 100,
		}, []string{"server"})
	}
	return &metrics{
		latency: histogram("latency_seconds",
			"Latency of successful stability probes."),
		jitter: histogram("jitter_seconds",
			"Absolute latency difference between consecutive successful stability probes."),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "librespeed",
			Subsystem: "stability",
			Name:      "requests_total",
			Help:      "Stability probes sent, by result.",
		}, []string{"server", "result"}),
	}
}

// init creates the series of a server, so counters start at zero.
func (m *metrics) init(server string) {
	m.latency.WithLabelValues(server)
	m.jitter.WithLabelValues(server)
	m.requests.WithLabelValues(server, "success")
	m.requests.WithLabelValues(server, "failure")
}

// remove drops every series of a server.
func (m *metrics) remove(server string) {
	labels := prometheus.Labels{"server": server}
	m.latency.DeletePartialMatch(labels)
	m.jitter.DeletePartialMatch(labels)
	m.requests.DeletePartialMatch(labels)
}

func (m *metrics) describe(ch chan<- *prometheus.Desc) {
	m.latency.Describe(ch)
	m.jitter.Describe(ch)
	m.requests.Describe(ch)
}

func (m *metrics) collect(ch chan<- prometheus.Metric) {
	m.latency.Collect(ch)
	m.jitter.Collect(ch)
	m.requests.Collect(ch)
}
