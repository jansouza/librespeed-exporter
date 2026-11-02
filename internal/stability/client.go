package stability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"

	"github.com/prometheus/common/version"

	"github.com/jansouza/librespeed-exporter/internal/config"
	"github.com/jansouza/librespeed-exporter/internal/httpclient"
)

// maxBody bounds what is read from a ping response, which should be empty.
const maxBody = 64 << 10

// client probes one server's ping path.
type client struct {
	http *http.Client
	url  string
}

// newClient builds a client for the server's ping path. See httpclient.New
// for the settings it honors.
func newClient(srv config.Server, test config.Test) (*client, error) {
	u, err := httpclient.URL(srv, srv.PingPath)
	if err != nil {
		return nil, err
	}
	hc, err := httpclient.New(srv, test)
	if err != nil {
		return nil, err
	}
	return &client{http: hc, url: u}, nil
}

// probe sends one request and returns the time from the request being
// written to the first response byte, which leaves out connection setup the
// way the web stability test does with the Performance API.
func (c *client) probe(ctx context.Context, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var wrote, firstByte atomic.Int64
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest:         func(httptrace.WroteRequestInfo) { wrote.Store(time.Now().UnixNano()) },
		GotFirstResponseByte: func() { firstByte.Store(time.Now().UnixNano()) },
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"?r="+randomToken(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("User-Agent", "librespeed-exporter/"+version.Version)

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	_, copyErr := io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status %s", resp.Status)
	}
	if copyErr != nil {
		return 0, copyErr
	}

	if w, f := wrote.Load(), firstByte.Load(); w > 0 && f > w {
		return time.Duration(f - w), nil
	}
	if elapsed := time.Since(start); elapsed > 0 {
		return elapsed, nil
	}
	return 0, errors.New("could not measure latency")
}

func (c *client) close() {
	c.http.CloseIdleConnections()
}

func randomToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
