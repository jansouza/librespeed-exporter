package runner

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/jansouza/librespeed-exporter/internal/config"
)

// serverID is the ID given to the single server in the list passed on stdin,
// and the one selected with --server.
const serverID = 1

// BuildArgs returns the librespeed-cli arguments for a test against srv.
func BuildArgs(test config.Test, srv config.Server) []string {
	args := []string{
		"--json",
		"--local-json", "-",
		"--server", strconv.Itoa(serverID),
		"--duration", seconds(test.Duration),
		"--concurrent", strconv.Itoa(test.Concurrent),
		"--chunks", strconv.Itoa(test.Chunks),
		"--upload-size", strconv.FormatInt(int64(test.UploadSize/config.KiB), 10),
		"--timeout", seconds(test.HTTPTimeout),
	}

	if !test.Download {
		args = append(args, "--no-download")
	}
	if !test.Upload {
		args = append(args, "--no-upload")
	}
	if test.PingMethod == config.PingHTTP {
		args = append(args, "--no-icmp")
	}
	switch test.IPVersion {
	case 4:
		args = append(args, "--ipv4")
	case 6:
		args = append(args, "--ipv6")
	}
	if test.SourceAddress != "" {
		args = append(args, "--source", test.SourceAddress)
	}
	if test.Interface != "" {
		args = append(args, "--interface", test.Interface)
	}
	if test.NoPreallocate {
		args = append(args, "--no-pre-allocate")
	}
	if srv.InsecureSkipVerify {
		args = append(args, "--skip-cert-verify")
	}
	if srv.CAFile != "" {
		args = append(args, "--ca-cert", srv.CAFile)
	}
	return args
}

func seconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10)
}

// cliServer is one entry of a librespeed-cli server list.
type cliServer struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Server   string `json:"server"`
	DLURL    string `json:"dlURL"`
	ULURL    string `json:"ulURL"`
	PingURL  string `json:"pingURL"`
	GetIPURL string `json:"getIpURL"`
}

// serverListJSON returns the server list fed to librespeed-cli on stdin: srv
// alone, so the CLI cannot pick any other server.
func serverListJSON(srv config.Server) ([]byte, error) {
	return json.Marshal([]cliServer{{
		ID:       serverID,
		Name:     srv.Name,
		Server:   srv.URL,
		DLURL:    srv.DownloadPath,
		ULURL:    srv.UploadPath,
		PingURL:  srv.PingPath,
		GetIPURL: srv.IPPath,
	}})
}
