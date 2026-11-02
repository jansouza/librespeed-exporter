package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadMinimalAppliesDefaults(t *testing.T) {
	cfg, warnings, err := Load("testdata/minimal.yml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}

	want := Default()
	want.Servers = []Server{{
		Name:         "home",
		URL:          "https://speed.example.com/",
		DownloadPath: DefaultDownloadPath,
		UploadPath:   DefaultUploadPath,
		PingPath:     DefaultPingPath,
		IPPath:       DefaultIPPath,
	}}
	if !reflect.DeepEqual(*cfg, want) {
		t.Errorf("config mismatch\n got: %+v\nwant: %+v", *cfg, want)
	}
}

func TestLoadFull(t *testing.T) {
	cfg, warnings, err := Load("testdata/full.yml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}

	want := Config{
		Interval:          time.Hour,
		RunOnStart:        false,
		Timeout:           90 * time.Second,
		CollectClientInfo: true,
		LibrespeedCLI:     CLI{Path: "/usr/local/bin/librespeed-cli"},
		Test: Test{
			Download:      true,
			Upload:        false,
			Duration:      20 * time.Second,
			Concurrent:    6,
			Chunks:        50,
			UploadSize:    4 * MiB,
			HTTPTimeout:   30 * time.Second,
			PingMethod:    PingICMP,
			IPVersion:     6,
			SourceAddress: "2001:db8::10",
			NoPreallocate: true,
		},
		Servers: []Server{
			{
				Name:         "home",
				URL:          "https://speed.example.com/",
				DownloadPath: DefaultDownloadPath,
				UploadPath:   DefaultUploadPath,
				PingPath:     DefaultPingPath,
				IPPath:       DefaultIPPath,
			},
			{
				Name:               "lab",
				URL:                "http://10.0.0.5:8080/",
				DownloadPath:       "garbage",
				UploadPath:         "empty",
				PingPath:           "empty",
				IPPath:             "getIP",
				InsecureSkipVerify: true,
			},
		},
	}
	if !reflect.DeepEqual(*cfg, want) {
		t.Errorf("config mismatch\n got: %+v\nwant: %+v", *cfg, want)
	}
}

func TestExampleFileIsValidAndMatchesDefaults(t *testing.T) {
	cfg, warnings, err := Load("../../librespeed-exporter.example.yml")
	if err != nil {
		t.Fatalf("the example configuration does not load: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	want := Default()
	want.Servers = cfg.Servers
	if !reflect.DeepEqual(*cfg, want) {
		t.Errorf("the example no longer documents the defaults\n got: %+v\nwant: %+v", *cfg, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, _, err := Load("testdata/does-not-exist.yml"); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestParsePartialTestKeepsOtherDefaults(t *testing.T) {
	cfg, _, err := Parse([]byte(`
test:
  concurrent: 8
servers:
  - name: a
    url: http://a/
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Default().Test
	want.Concurrent = 8
	if cfg.Test != want {
		t.Errorf("test section mismatch\n got: %+v\nwant: %+v", cfg.Test, want)
	}
}

func TestParseNullSectionKeepsDefaults(t *testing.T) {
	cfg, _, err := Parse([]byte("test:\nlibrespeed_cli:\nservers:\n  - name: a\n    url: http://a/\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if want := Default().Test; cfg.Test != want {
		t.Errorf("test section mismatch\n got: %+v\nwant: %+v", cfg.Test, want)
	}
}

func TestParseCAFile(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte("not checked"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Parse([]byte("servers:\n  - name: a\n    url: https://a/\n    ca_file: " + ca + "\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Servers[0].CAFile != ca {
		t.Errorf("ca_file = %q, want %q", cfg.Servers[0].CAFile, ca)
	}
}

func TestParseErrors(t *testing.T) {
	const server = "servers:\n  - name: a\n    url: http://a/\n"

	tests := []struct {
		name string
		yaml string
		want []string // substrings that must all appear in the error
	}{
		{"empty document", "", []string{"servers: at least one server"}},
		{"empty server list", "servers: []\n", []string{"servers: at least one server"}},
		{"unknown top-level field", "intervall: 1h\n" + server, []string{"intervall"}},
		{"unknown server field", "servers:\n  - name: a\n    url: http://a/\n    dl_path: x\n", []string{"dl_path"}},
		{"integer duration", "interval: 60\n" + server, []string{"parsing YAML"}},
		{"zero interval", "interval: 0s\n" + server, []string{"interval: must be greater than zero"}},
		{"zero timeout", "timeout: 0s\n" + server, []string{"timeout: must be greater than zero"}},
		{"fractional duration", "test:\n  duration: 1500ms\n" + server, []string{"test.duration: must be a whole number of seconds"}},
		{"sub-second duration", "test:\n  duration: 500ms\n" + server, []string{"test.duration: must be at least 1s"}},
		{"fractional http timeout", "test:\n  http_timeout: 2.5s\n" + server, []string{"test.http_timeout: must be a whole number"}},
		{"zero concurrent", "test:\n  concurrent: 0\n" + server, []string{"test.concurrent: must be at least 1"}},
		{"zero chunks", "test:\n  chunks: 0\n" + server, []string{"test.chunks: must be at least 1"}},
		{"upload size not KiB multiple", "test:\n  upload_size: 1500\n" + server, []string{"test.upload_size: must be a positive multiple of 1KiB"}},
		{"upload size zero", "test:\n  upload_size: 0KiB\n" + server, []string{"test.upload_size"}},
		{"upload size bad unit", "test:\n  upload_size: 1MB\n" + server, []string{`unknown unit "MB"`}},
		{"bad ping method", "test:\n  ping_method: tcp\n" + server, []string{"test.ping_method"}},
		{"bad ip version", "test:\n  ip_version: 5\n" + server, []string{"test.ip_version: must be 0, 4 or 6"}},
		{"bad source address", "test:\n  source_address: eth0\n" + server, []string{"test.source_address"}},
		{"source and interface", "test:\n  source_address: 192.0.2.1\n  interface: eth0\n" + server, []string{"cannot be set together"}},
		{"timeout too short", "timeout: 40s\n" + server, []string{"timeout: must be at least 45s"}},
		{"empty server name", "servers:\n  - url: http://a/\n", []string{"servers[0].name: must not be empty"}},
		{"duplicate server name", "servers:\n  - name: a\n    url: http://a/\n  - name: a\n    url: http://b/\n", []string{`servers[1].name: "a" is already used by servers[0]`}},
		{"empty url", "servers:\n  - name: a\n", []string{"servers[0].url: must not be empty"}},
		{"ftp url", "servers:\n  - name: a\n    url: ftp://a/\n", []string{"servers[0].url: scheme must be http or https"}},
		{"url without host", "servers:\n  - name: a\n    url: http:///path\n", []string{"has no host"}},
		{"missing ca file", "servers:\n  - name: a\n    url: https://a/\n    ca_file: /nonexistent/ca.pem\n", []string{"servers[0].ca_file"}},
		{
			"reports every problem",
			"test:\n  concurrent: 0\n  chunks: 0\nservers:\n  - name: a\n    url: ftp://a/\n",
			[]string{"test.concurrent", "test.chunks", "servers[0].url"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestParseTimeoutDependsOnEnabledPhases(t *testing.T) {
	// With only download enabled, 15s of test plus the 15s margin fits in 30s.
	_, _, err := Parse([]byte("timeout: 30s\ntest:\n  upload: false\nservers:\n  - name: a\n    url: http://a/\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

func TestParseWarnsWhenIntervalShorterThanCycle(t *testing.T) {
	_, warnings, err := Parse([]byte(`
interval: 3m
timeout: 2m
servers:
  - name: a
    url: http://a/
  - name: b
    url: http://b/
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "interval (3m0s) is shorter") {
		t.Errorf("warnings = %q, want one about the interval", warnings)
	}
}
