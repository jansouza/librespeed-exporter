// Package config loads and validates the librespeed-exporter configuration
// file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// PingMethod selects how librespeed-cli measures latency.
type PingMethod string

const (
	// PingHTTP always measures latency over HTTP (--no-icmp).
	PingHTTP PingMethod = "http"
	// PingICMP tries ICMP first and falls back to HTTP, as librespeed-cli
	// does by default.
	PingICMP PingMethod = "icmp"
)

// Default paths of the LibreSpeed PHP backend, relative to the server URL.
const (
	DefaultDownloadPath = "backend/garbage.php"
	DefaultUploadPath   = "backend/empty.php"
	DefaultPingPath     = "backend/empty.php"
	DefaultIPPath       = "backend/getIP.php"
)

// Config is the whole configuration file.
type Config struct {
	Interval          time.Duration `yaml:"interval"`
	RunOnStart        bool          `yaml:"run_on_start"`
	Timeout           time.Duration `yaml:"timeout"`
	CollectClientInfo bool          `yaml:"collect_client_info"`
	LibrespeedCLI     CLI           `yaml:"librespeed_cli"`
	Test              Test          `yaml:"test"`
	Stability         Stability     `yaml:"stability"`
	Servers           []Server      `yaml:"servers"`
}

// Stability configures continuous latency probing, the equivalent of the
// LibreSpeed web stability test. It sends small HTTP requests to each
// server's ping path, independently of librespeed-cli.
type Stability struct {
	Enabled bool `yaml:"enabled"`
	// Interval is the minimum time between the start of two probes to the
	// same server.
	Interval time.Duration `yaml:"interval"`
	// Timeout bounds each probe; a probe that times out counts as failed.
	Timeout time.Duration `yaml:"timeout"`
}

// MinStabilityInterval is the shortest allowed stability.interval, the same
// floor the LibreSpeed web stability test uses.
const MinStabilityInterval = 200 * time.Millisecond

// CLI locates the librespeed-cli binary.
type CLI struct {
	// Path to the binary. Empty means looking up librespeed-cli in PATH.
	Path string `yaml:"path"`
}

// Test holds the parameters passed to librespeed-cli on every run.
type Test struct {
	Download      bool          `yaml:"download"`
	Upload        bool          `yaml:"upload"`
	Duration      time.Duration `yaml:"duration"`
	Concurrent    int           `yaml:"concurrent"`
	Chunks        int           `yaml:"chunks"`
	UploadSize    Size          `yaml:"upload_size"`
	HTTPTimeout   time.Duration `yaml:"http_timeout"`
	PingMethod    PingMethod    `yaml:"ping_method"`
	IPVersion     int           `yaml:"ip_version"`
	SourceAddress string        `yaml:"source_address"`
	Interface     string        `yaml:"interface"`
	NoPreallocate bool          `yaml:"no_preallocate"`
}

// Server is a LibreSpeed server to test against.
type Server struct {
	// Name identifies the server and becomes the "server" label.
	Name               string `yaml:"name"`
	URL                string `yaml:"url"`
	DownloadPath       string `yaml:"download_path"`
	UploadPath         string `yaml:"upload_path"`
	PingPath           string `yaml:"ping_path"`
	IPPath             string `yaml:"ip_path"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
	CAFile             string `yaml:"ca_file"`
}

// Default returns the configuration used for every field the file leaves
// out. The test parameters match the librespeed-cli defaults, except for the
// ping method.
func Default() Config {
	return Config{
		Interval:   30 * time.Minute,
		RunOnStart: true,
		Timeout:    2 * time.Minute,
		Test: Test{
			Download:    true,
			Upload:      true,
			Duration:    15 * time.Second,
			Concurrent:  3,
			Chunks:      100,
			UploadSize:  1024 * KiB,
			HTTPTimeout: 15 * time.Second,
			PingMethod:  PingHTTP,
		},
		Stability: Stability{
			Interval: time.Second,
			Timeout:  5 * time.Second,
		},
	}
}

// Load reads, validates and returns the configuration file at path. Warnings
// describe settings that are allowed but probably not intended.
func Load(path string) (*Config, []string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the path comes from the operator.
	if err != nil {
		return nil, nil, fmt.Errorf("reading config file: %w", err)
	}
	cfg, warnings, err := Parse(data)
	if err != nil {
		return nil, nil, fmt.Errorf("loading %s: %w", path, err)
	}
	return cfg, warnings, nil
}

// Parse decodes and validates a configuration document.
func Parse(data []byte) (*Config, []string, error) {
	cfg := Default()

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("parsing YAML: %w", err)
	}

	cfg.applyServerDefaults()

	warnings, err := cfg.Validate()
	if err != nil {
		return nil, nil, err
	}
	return &cfg, warnings, nil
}

func (c *Config) applyServerDefaults() {
	for i := range c.Servers {
		s := &c.Servers[i]
		if s.DownloadPath == "" {
			s.DownloadPath = DefaultDownloadPath
		}
		if s.UploadPath == "" {
			s.UploadPath = DefaultUploadPath
		}
		if s.PingPath == "" {
			s.PingPath = DefaultPingPath
		}
		if s.IPPath == "" {
			s.IPPath = DefaultIPPath
		}
	}
}
