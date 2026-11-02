package collector

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "librespeed"

func newDesc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(prometheus.BuildFQName(namespace, "", name), help, labels, nil)
}

var (
	downloadDesc = newDesc("download_bytes_per_second",
		"Download speed measured in the last successful test.", "server")
	uploadDesc = newDesc("upload_bytes_per_second",
		"Upload speed measured in the last successful test.", "server")
	pingDesc = newDesc("ping_seconds",
		"Mean latency measured in the last successful test.", "server")
	jitterDesc = newDesc("jitter_seconds",
		"Jitter measured in the last successful test.", "server")

	upDesc = newDesc("up",
		"Whether the last test against the server succeeded.", "server")
	lastTestDesc = newDesc("last_test_timestamp_seconds",
		"Unix time the last test finished, successful or not.", "server")
	lastSuccessDesc = newDesc("last_success_timestamp_seconds",
		"Unix time the last successful test finished.", "server")
	durationDesc = newDesc("test_duration_seconds",
		"How long the last librespeed-cli run took.", "server")
	testsDesc = newDesc("tests_total",
		"Number of tests run, by result.", "server", "result")
	transferredDesc = newDesc("transferred_bytes_total",
		"Bytes transferred by successful tests, by direction.", "server", "direction")
	runningDesc = newDesc("test_running",
		"Whether a librespeed-cli process is running.")
	reloadDesc = newDesc("config_last_reload_successful",
		"Whether the last configuration reload succeeded.")

	serverInfoDesc = newDesc("server_info",
		"Configured LibreSpeed server, always 1.", "server", "url")
	clientInfoDesc = newDesc("client_info",
		"Public IP and ISP seen by the server in the last successful test, always 1.", "server", "ip", "isp")
	cliInfoDesc = newDesc("cli_info",
		"Version of the librespeed-cli binary in use, always 1.", "version")
)

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		downloadDesc, uploadDesc, pingDesc, jitterDesc,
		upDesc, lastTestDesc, lastSuccessDesc, durationDesc, testsDesc, transferredDesc,
		runningDesc, reloadDesc, serverInfoDesc, clientInfoDesc, cliInfoDesc,
	} {
		ch <- d
	}
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	gauge := func(desc *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v, labels...)
	}
	counter := func(desc *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, v, labels...)
	}

	gauge(runningDesc, boolToFloat(c.running))
	gauge(reloadDesc, boolToFloat(c.reloadSuccessful))
	if c.cliVersion != "" {
		gauge(cliInfoDesc, 1, c.cliVersion)
	}

	for _, name := range c.order {
		st := c.servers[name]

		gauge(serverInfoDesc, 1, name, st.url)
		counter(testsDesc, st.successes, name, "success")
		counter(testsDesc, st.failures, name, "failure")
		counter(transferredDesc, st.bytesDown, name, "download")
		counter(transferredDesc, st.bytesUp, name, "upload")

		if st.tested {
			gauge(upDesc, boolToFloat(st.up), name)
			gauge(lastTestDesc, unixSeconds(st.lastTest), name)
			gauge(durationDesc, st.lastDuration.Seconds(), name)
		}

		if st.lastSuccess.IsZero() {
			continue
		}
		res := st.last
		gauge(lastSuccessDesc, unixSeconds(st.lastSuccess), name)
		gauge(pingDesc, res.PingSeconds, name)
		gauge(jitterDesc, res.JitterSeconds, name)
		// A phase must be enabled now and have been measured by the result,
		// so neither a disabled phase nor one just re-enabled shows a zero.
		if c.download && st.hasDownload {
			gauge(downloadDesc, res.DownloadBytesPerSecond, name)
		}
		if c.upload && st.hasUpload {
			gauge(uploadDesc, res.UploadBytesPerSecond, name)
		}
		if c.collectClientInfo && (res.ClientIP != "" || res.ISP != "") {
			gauge(clientInfoDesc, 1, name, res.ClientIP, res.ISP)
		}
	}
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func unixSeconds(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e9
}
