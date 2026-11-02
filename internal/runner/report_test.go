package runner

import (
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/jansouza/librespeed-exporter/internal/config"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b))
}

func TestParseReportFixture(t *testing.T) {
	// testdata/report.json is real librespeed-cli v1.0.14 output against a
	// local LibreSpeed container.
	var res Result
	if err := parseReport(readFixture(t, "report.json"), config.Default().Test, &res); err != nil {
		t.Fatalf("parseReport: %v", err)
	}

	checks := []struct {
		name      string
		got, want float64
	}{
		{"download", res.DownloadBytesPerSecond, 19194.27e6 / 8},
		{"upload", res.UploadBytesPerSecond, 12644.38e6 / 8},
		{"ping", res.PingSeconds, 0.00021},
		{"jitter", res.JitterSeconds, 0.00004},
	}
	for _, c := range checks {
		if !approxEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if res.BytesReceived != 8646180864 || res.BytesSent != 5692882944 {
		t.Errorf("bytes = %d received, %d sent", res.BytesReceived, res.BytesSent)
	}
}

func TestConversionsAreExact(t *testing.T) {
	// Exact comparisons on purpose: these values are written to /metrics.
	if got := msToSeconds(0.03); got != 3e-05 {
		t.Errorf("msToSeconds(0.03) = %v", got)
	}
	if got := msToSeconds(12.34); got != 0.01234 {
		t.Errorf("msToSeconds(12.34) = %v", got)
	}
	if got := mbpsToBytesPerSecond(495.03); got != 61878750 {
		t.Errorf("mbpsToBytesPerSecond(495.03) = %v", got)
	}
}

func TestParseReportClientInfoAndUnknownFields(t *testing.T) {
	out := `[{"timestamp":"2026-11-02T10:15:03-03:00","server":{"name":"a","url":"https://a/"},
	"client":{"ip":"203.0.113.10","org":"AS64500 Example Telecom","hostname":""},
	"bytes_sent":1,"bytes_received":2,"ping":12.34,"jitter":1.02,"upload":480.12,"download":495.03,"share":"",
	"tls":{"version":"TLS 1.3","cipher":"TLS_AES_128_GCM_SHA256"},"field_from_the_future":{"x":1}}]`
	var res Result
	if err := parseReport([]byte(out), config.Default().Test, &res); err != nil {
		t.Fatalf("parseReport: %v", err)
	}
	if res.ClientIP != "203.0.113.10" || res.ISP != "AS64500 Example Telecom" {
		t.Errorf("client = %q, %q", res.ClientIP, res.ISP)
	}
	if !approxEqual(res.PingSeconds, 0.01234) {
		t.Errorf("ping = %v", res.PingSeconds)
	}
}

func TestParseReportUploadDisabled(t *testing.T) {
	test := config.Default().Test
	test.Upload = false
	var res Result
	if err := parseReport(readFixture(t, "report-no-upload.json"), test, &res); err != nil {
		t.Fatalf("parseReport: %v", err)
	}
	if res.UploadBytesPerSecond != 0 || res.BytesSent != 0 {
		t.Errorf("upload = %v, bytes sent = %d, want zero", res.UploadBytesPerSecond, res.BytesSent)
	}
}

func TestParseReportErrors(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		wantErr error  // checked with errors.Is when set
		wantMsg string // substring of the error otherwise
	}{
		{name: "server down (real output)", out: string(readFixture(t, "server-down.json")), wantErr: ErrNoReport},
		{name: "empty array", out: "[]", wantErr: ErrNoReport},
		{name: "empty output", out: "  \n", wantMsg: "nothing to stdout"},
		{name: "truncated", out: `[{"ping":1.0,"download":`, wantMsg: "decoding"},
		{name: "not an array", out: `{"ping":1}`, wantMsg: "decoding"},
		{name: "two reports", out: `[{"download":1,"upload":1},{"download":1,"upload":1}]`, wantMsg: "got 2"},
		{name: "zero download", out: `[{"download":0,"upload":1}]`, wantMsg: "download speed of zero"},
		{name: "zero upload", out: string(readFixture(t, "report-no-upload.json")), wantMsg: "upload speed of zero"},
		{name: "negative ping", out: `[{"ping":-1,"download":1,"upload":1}]`, wantMsg: "negative ping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var res Result
			err := parseReport([]byte(tt.out), config.Default().Test, &res)
			switch {
			case err == nil:
				t.Fatal("expected an error")
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			case tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg):
				t.Errorf("error = %q, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}
