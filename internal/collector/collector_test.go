package collector

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jansouza/librespeed-exporter/internal/config"
	"github.com/jansouza/librespeed-exporter/internal/runner"
)

func testConfig() *config.Config {
	cfg := config.Default()
	cfg.Servers = []config.Server{
		{Name: "home", URL: "https://speed.example.com/"},
		{Name: "lab", URL: "http://10.0.0.5:8080/"},
	}
	return &cfg
}

var start = time.Unix(1700000000, 0)

func successResult() runner.Result {
	return runner.Result{
		Server:                 "home",
		StartedAt:              start,
		FinishedAt:             start.Add(35500 * time.Millisecond),
		DownloadBytesPerSecond: 61878750,
		UploadBytesPerSecond:   60015000,
		PingSeconds:            0.01234,
		JitterSeconds:          0.00102,
		BytesReceived:          953155584,
		BytesSent:              912261120,
		ClientIP:               "203.0.113.10",
		ISP:                    "AS64500 Example Telecom",
	}
}

func failureResult() runner.Result {
	failStart := start.Add(30 * time.Minute)
	return runner.Result{Server: "home", StartedAt: failStart, FinishedAt: failStart.Add(15 * time.Second)}
}

func compareGolden(t *testing.T, c *Collector, golden string) {
	t.Helper()
	data, err := os.ReadFile("testdata/" + golden)
	if err != nil {
		t.Fatal(err)
	}
	if err := testutil.CollectAndCompare(c, bytes.NewReader(data)); err != nil {
		t.Error(err)
	}
}

func compare(t *testing.T, c *Collector, expected string, names ...string) {
	t.Helper()
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), names...); err != nil {
		t.Error(err)
	}
}

func TestInitialState(t *testing.T) {
	compareGolden(t, New(testConfig()), "initial.prom")
}

func TestAfterSuccess(t *testing.T) {
	c := New(testConfig())
	c.SetCLIVersion("1.0.14")
	c.RecordSuccess(successResult())
	compareGolden(t, c, "after_success.prom")
}

func TestFailureKeepsLastResult(t *testing.T) {
	c := New(testConfig())
	c.SetCLIVersion("1.0.14")
	c.RecordSuccess(successResult())
	c.RecordFailure(failureResult())
	compareGolden(t, c, "after_failure.prom")
}

func TestFailureBeforeAnySuccess(t *testing.T) {
	c := New(testConfig())
	c.RecordFailure(failureResult())
	compare(t, c, `
# HELP librespeed_up Whether the last test against the server succeeded.
# TYPE librespeed_up gauge
librespeed_up{server="home"} 0
`, "librespeed_up", "librespeed_last_success_timestamp_seconds", "librespeed_download_bytes_per_second", "librespeed_ping_seconds")
}

func TestCountersAccumulate(t *testing.T) {
	c := New(testConfig())
	c.RecordSuccess(successResult())
	c.RecordFailure(failureResult())
	c.RecordSuccess(successResult())
	compare(t, c, `
# HELP librespeed_tests_total Number of tests run, by result.
# TYPE librespeed_tests_total counter
librespeed_tests_total{result="failure",server="home"} 1
librespeed_tests_total{result="success",server="home"} 2
librespeed_tests_total{result="failure",server="lab"} 0
librespeed_tests_total{result="success",server="lab"} 0
# HELP librespeed_transferred_bytes_total Bytes transferred by successful tests, by direction.
# TYPE librespeed_transferred_bytes_total counter
librespeed_transferred_bytes_total{direction="download",server="home"} 1.906311168e+09
librespeed_transferred_bytes_total{direction="upload",server="home"} 1.82452224e+09
librespeed_transferred_bytes_total{direction="download",server="lab"} 0
librespeed_transferred_bytes_total{direction="upload",server="lab"} 0
`, "librespeed_tests_total", "librespeed_transferred_bytes_total")
}

func TestDisabledPhaseIsNotExposed(t *testing.T) {
	cfg := testConfig()
	cfg.Test.Upload = false
	c := New(cfg)
	res := successResult()
	res.UploadBytesPerSecond = 0
	c.RecordSuccess(res)
	compare(t, c, `
# HELP librespeed_download_bytes_per_second Download speed measured in the last successful test.
# TYPE librespeed_download_bytes_per_second gauge
librespeed_download_bytes_per_second{server="home"} 6.187875e+07
`, "librespeed_download_bytes_per_second", "librespeed_upload_bytes_per_second")
}

func TestReenabledPhaseWaitsForNextTest(t *testing.T) {
	cfg := testConfig()
	cfg.Test.Upload = false
	c := New(cfg)
	c.RecordSuccess(successResult())

	cfg.Test.Upload = true
	c.SetConfig(cfg)
	compare(t, c, "", "librespeed_upload_bytes_per_second")

	c.RecordSuccess(successResult())
	compare(t, c, `
# HELP librespeed_upload_bytes_per_second Upload speed measured in the last successful test.
# TYPE librespeed_upload_bytes_per_second gauge
librespeed_upload_bytes_per_second{server="home"} 6.0015e+07
`, "librespeed_upload_bytes_per_second")
}

func TestClientInfo(t *testing.T) {
	cfg := testConfig()
	c := New(cfg)
	c.RecordSuccess(successResult())
	compare(t, c, "", "librespeed_client_info")

	cfg.CollectClientInfo = true
	c.SetConfig(cfg)
	compare(t, c, `
# HELP librespeed_client_info Public IP and ISP seen by the server in the last successful test, always 1.
# TYPE librespeed_client_info gauge
librespeed_client_info{ip="203.0.113.10",isp="AS64500 Example Telecom",server="home"} 1
`, "librespeed_client_info")

	// A server that reports no client details (a local container, for
	// instance) does not produce an all-empty series.
	res := successResult()
	res.ClientIP, res.ISP = "", ""
	c.RecordSuccess(res)
	compare(t, c, "", "librespeed_client_info")
}

func TestSetConfigDropsRemovedServersAndKeepsOthers(t *testing.T) {
	cfg := testConfig()
	c := New(cfg)
	c.RecordSuccess(successResult())
	lab := successResult()
	lab.Server = "lab"
	c.RecordSuccess(lab)

	cfg.Servers = []config.Server{{Name: "lab", URL: "http://10.0.0.6:8080/"}}
	c.SetConfig(cfg)
	compare(t, c, `
# HELP librespeed_server_info Configured LibreSpeed server, always 1.
# TYPE librespeed_server_info gauge
librespeed_server_info{server="lab",url="http://10.0.0.6:8080/"} 1
# HELP librespeed_up Whether the last test against the server succeeded.
# TYPE librespeed_up gauge
librespeed_up{server="lab"} 1
`, "librespeed_server_info", "librespeed_up")
}

func TestResultForUnknownServerIsIgnored(t *testing.T) {
	c := New(testConfig())
	res := successResult()
	res.Server = "removed-during-run"
	c.RecordSuccess(res)
	c.RecordFailure(res)
	compareGolden(t, c, "initial.prom")
}

func TestProcessGauges(t *testing.T) {
	c := New(testConfig())
	c.SetRunning(true)
	c.SetReloadSuccessful(false)
	compare(t, c, `
# HELP librespeed_config_last_reload_successful Whether the last configuration reload succeeded.
# TYPE librespeed_config_last_reload_successful gauge
librespeed_config_last_reload_successful 0
# HELP librespeed_test_running Whether a librespeed-cli process is running.
# TYPE librespeed_test_running gauge
librespeed_test_running 1
`, "librespeed_config_last_reload_successful", "librespeed_test_running")
}

func TestMetricsPassLint(t *testing.T) {
	c := New(testConfig())
	c.SetCLIVersion("1.0.14")
	c.RecordSuccess(successResult())
	problems, err := testutil.CollectAndLint(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("%s: %s", p.Metric, p.Text)
	}
}
