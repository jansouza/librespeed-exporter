// Package clientinfo asks a LibreSpeed server for the public IP and ISP it
// sees, through the server's ip_path (getIP.php).
//
// librespeed-cli reads both from the server's rawIspInfo object. A server
// that does not look up the ISP answers with an empty rawIspInfo, and the CLI
// then reports no IP either, although the server still sends the IP in
// processedString. The scheduler uses Fetch to fill that gap.
package clientinfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"

	"github.com/prometheus/common/version"

	"github.com/jansouza/librespeed-exporter/internal/config"
	"github.com/jansouza/librespeed-exporter/internal/httpclient"
)

// maxBody bounds what is read from a getIP.php response, a few hundred bytes.
const maxBody = 64 << 10

// ErrNotPublic means the server saw a private, loopback or link-local
// address, as servers on the local network do. It is not reported.
var ErrNotPublic = errors.New("server sees a non-public address")

// Info is what the server sees. ISP is empty when the server did not look it
// up.
type Info struct {
	IP  string
	ISP string
}

// Fetch requests the server's ip_path with isp=true and parses the answer.
func Fetch(ctx context.Context, srv config.Server, test config.Test) (Info, error) {
	u, err := httpclient.URL(srv, srv.IPPath)
	if err != nil {
		return Info{}, err
	}
	hc, err := httpclient.New(srv, test)
	if err != nil {
		return Info{}, err
	}
	defer hc.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?isp=true", nil)
	if err != nil {
		return Info{}, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("User-Agent", "librespeed-exporter/"+version.Version)

	resp, err := hc.Do(req)
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Info{}, err
	}
	return parse(body)
}

// response is the JSON of the LibreSpeed PHP backend. rawIspInfo is an
// ipinfo.io object when the server looked up the ISP, and "" when it did not.
type response struct {
	ProcessedString string          `json:"processedString"`
	RawISPInfo      json.RawMessage `json:"rawIspInfo"`
}

type rawISPInfo struct {
	IP  string `json:"ip"`
	Org string `json:"org"`
}

// parse reads the IP and ISP from rawIspInfo, falling back to the IP at the
// start of processedString ("203.0.113.7 - Unknown ISP"). Older backends
// answer with that string as plain text instead of JSON.
func parse(body []byte) (Info, error) {
	var info Info
	processed := strings.TrimSpace(string(body))
	var r response
	if json.Unmarshal(body, &r) == nil {
		processed = r.ProcessedString
		var raw rawISPInfo
		if json.Unmarshal(r.RawISPInfo, &raw) == nil {
			info = Info{IP: raw.IP, ISP: raw.Org}
		}
	}
	if info.IP == "" {
		info.IP, _, _ = strings.Cut(processed, " - ")
	}

	ip, err := netip.ParseAddr(strings.TrimSpace(info.IP))
	if err != nil {
		return Info{}, fmt.Errorf("no IP address in response %q", truncate(processed, 100))
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return Info{}, fmt.Errorf("%w: %s", ErrNotPublic, ip)
	}
	info.IP = ip.String()
	return info, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
