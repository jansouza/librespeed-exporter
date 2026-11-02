package stability

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path"
	"sync/atomic"
	"time"

	"github.com/prometheus/common/version"

	"github.com/jansouza/librespeed-exporter/internal/config"
)

// maxBody bounds what is read from a ping response, which should be empty.
const maxBody = 64 << 10

// client probes one server's ping path.
type client struct {
	http *http.Client
	url  string
}

// newClient builds an HTTP client that honors the server's TLS settings and
// the test's ip_version and source_address. The interface setting is not
// applied: binding to an interface is OS-specific and librespeed-cli does it
// on its own.
func newClient(srv config.Server, test config.Test) (*client, error) {
	u, err := url.Parse(srv.URL)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, srv.PingPath)

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: srv.InsecureSkipVerify, // #nosec G402 -- opt-in per server, mirrors --skip-cert-verify.
	}
	if srv.CAFile != "" {
		pem, err := os.ReadFile(srv.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_file %s contains no PEM certificate", srv.CAFile)
		}
		tlsConfig.RootCAs = pool
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if test.SourceAddress != "" {
		dialer.LocalAddr = &net.TCPAddr{IP: net.ParseIP(test.SourceAddress)}
	}
	network := "tcp"
	switch test.IPVersion {
	case 4:
		network = "tcp4"
	case 6:
		network = "tcp6"
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig:     tlsConfig,
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &client{http: &http.Client{Transport: transport}, url: u.String()}, nil
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
