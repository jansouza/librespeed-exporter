package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/jansouza/librespeed-exporter/internal/config"
)

// ErrNoReport means librespeed-cli exited successfully without a report. It
// does that when the server fails its initial availability check.
var ErrNoReport = errors.New("librespeed-cli returned no report; the server did not respond")

// report holds the fields of a librespeed-cli --json report that the
// exporter uses. Unknown fields are ignored so newer CLI versions still parse.
type report struct {
	Client struct {
		IP  string `json:"ip"`
		Org string `json:"org"`
	} `json:"client"`
	BytesSent     uint64  `json:"bytes_sent"`
	BytesReceived uint64  `json:"bytes_received"`
	Ping          float64 `json:"ping"`     // milliseconds
	Jitter        float64 `json:"jitter"`   // milliseconds
	Upload        float64 `json:"upload"`   // megabits per second
	Download      float64 `json:"download"` // megabits per second
}

// parseReport decodes the stdout of a librespeed-cli --json run that tested
// exactly one server, and fills the measurement fields of res.
func parseReport(stdout []byte, test config.Test, res *Result) error {
	stdout = bytes.TrimSpace(stdout)
	if len(stdout) == 0 {
		return errors.New("librespeed-cli wrote nothing to stdout")
	}

	var reports []report
	if err := json.Unmarshal(stdout, &reports); err != nil {
		return fmt.Errorf("decoding librespeed-cli output: %w", err)
	}
	switch len(reports) {
	case 0:
		return ErrNoReport
	case 1:
	default:
		return fmt.Errorf("expected 1 report from librespeed-cli, got %d", len(reports))
	}
	r := reports[0]

	for _, v := range []struct {
		name  string
		value float64
	}{{"ping", r.Ping}, {"jitter", r.Jitter}, {"download", r.Download}, {"upload", r.Upload}} {
		if v.value < 0 {
			return fmt.Errorf("librespeed-cli reported a negative %s: %v", v.name, v.value)
		}
	}
	if test.Download && r.Download == 0 {
		return errors.New("librespeed-cli reported a download speed of zero")
	}
	if test.Upload && r.Upload == 0 {
		return errors.New("librespeed-cli reported an upload speed of zero")
	}

	res.DownloadBytesPerSecond = mbpsToBytesPerSecond(r.Download)
	res.UploadBytesPerSecond = mbpsToBytesPerSecond(r.Upload)
	res.PingSeconds = msToSeconds(r.Ping)
	res.JitterSeconds = msToSeconds(r.Jitter)
	res.BytesReceived = r.BytesReceived
	res.BytesSent = r.BytesSent
	res.ClientIP = r.Client.IP
	res.ISP = r.Client.Org
	return nil
}

// librespeed-cli rounds every value to two decimals. The conversions work on
// the value in hundredths, an integer, so 0.03 ms becomes 3e-05 s instead of
// 2.9999999999999997e-05.

func mbpsToBytesPerSecond(mbps float64) float64 {
	return math.Round(mbps*100) * 1e4 / 8
}

func msToSeconds(ms float64) float64 {
	return math.Round(ms*100) / 1e5
}
