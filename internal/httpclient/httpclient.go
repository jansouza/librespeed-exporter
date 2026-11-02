// Package httpclient builds the HTTP clients the exporter uses to talk to a
// LibreSpeed server directly, outside librespeed-cli: stability probes and
// the client info lookup.
package httpclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"time"

	"github.com/jansouza/librespeed-exporter/internal/config"
)

// New builds an HTTP client that honors the server's TLS settings and the
// test's ip_version and source_address. The interface setting is not
// applied: binding to an interface is OS-specific and librespeed-cli does it
// on its own.
func New(srv config.Server, test config.Test) (*http.Client, error) {
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
	return &http.Client{Transport: transport}, nil
}

// URL joins one of the server's paths (ping_path, ip_path) to its base URL.
func URL(srv config.Server, p string) (string, error) {
	u, err := url.Parse(srv.URL)
	if err != nil {
		return "", err
	}
	u.Path = path.Join(u.Path, p)
	return u.String(), nil
}
