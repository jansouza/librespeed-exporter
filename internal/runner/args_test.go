package runner

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jansouza/librespeed-exporter/internal/config"
)

func testServer() config.Server {
	return config.Server{
		Name:         "home",
		URL:          "https://speed.example.com/",
		DownloadPath: config.DefaultDownloadPath,
		UploadPath:   config.DefaultUploadPath,
		PingPath:     config.DefaultPingPath,
		IPPath:       config.DefaultIPPath,
	}
}

var baseArgs = []string{
	"--json", "--local-json", "-", "--server", "1",
	"--duration", "15", "--concurrent", "3", "--chunks", "100",
	"--upload-size", "1024", "--timeout", "15",
}

func TestBuildArgs(t *testing.T) {
	tests := []struct {
		name   string
		test   func(*config.Test)
		server func(*config.Server)
		extra  []string
	}{
		{name: "defaults", extra: []string{"--no-icmp"}},
		{name: "icmp", test: func(t *config.Test) { t.PingMethod = config.PingICMP }},
		{name: "no download", test: func(t *config.Test) { t.Download = false }, extra: []string{"--no-download", "--no-icmp"}},
		{name: "no upload", test: func(t *config.Test) { t.Upload = false }, extra: []string{"--no-upload", "--no-icmp"}},
		{name: "ipv4", test: func(t *config.Test) { t.IPVersion = 4 }, extra: []string{"--no-icmp", "--ipv4"}},
		{name: "ipv6", test: func(t *config.Test) { t.IPVersion = 6 }, extra: []string{"--no-icmp", "--ipv6"}},
		{name: "source", test: func(t *config.Test) { t.SourceAddress = "192.0.2.1" }, extra: []string{"--no-icmp", "--source", "192.0.2.1"}},
		{name: "interface", test: func(t *config.Test) { t.Interface = "eth0" }, extra: []string{"--no-icmp", "--interface", "eth0"}},
		{name: "no preallocate", test: func(t *config.Test) { t.NoPreallocate = true }, extra: []string{"--no-icmp", "--no-pre-allocate"}},
		{name: "skip cert verify", server: func(s *config.Server) { s.InsecureSkipVerify = true }, extra: []string{"--no-icmp", "--skip-cert-verify"}},
		{name: "ca file", server: func(s *config.Server) { s.CAFile = "/etc/ca.pem" }, extra: []string{"--no-icmp", "--ca-cert", "/etc/ca.pem"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			test := config.Default().Test
			if tt.test != nil {
				tt.test(&test)
			}
			srv := testServer()
			if tt.server != nil {
				tt.server(&srv)
			}
			want := append(append([]string{}, baseArgs...), tt.extra...)
			if got := BuildArgs(test, srv); !reflect.DeepEqual(got, want) {
				t.Errorf("BuildArgs()\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

func TestBuildArgsConvertsUnits(t *testing.T) {
	test := config.Default().Test
	test.Duration = 20e9
	test.HTTPTimeout = 30e9
	test.UploadSize = 4 * config.MiB
	got := BuildArgs(test, testServer())
	for _, pair := range [][2]string{{"--duration", "20"}, {"--timeout", "30"}, {"--upload-size", "4096"}} {
		if !hasPair(got, pair[0], pair[1]) {
			t.Errorf("BuildArgs() = %q, want %s %s", got, pair[0], pair[1])
		}
	}
}

func hasPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestServerListJSON(t *testing.T) {
	data, err := serverListJSON(testServer())
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{
		"id":       float64(1),
		"name":     "home",
		"server":   "https://speed.example.com/",
		"dlURL":    "backend/garbage.php",
		"ulURL":    "backend/empty.php",
		"pingURL":  "backend/empty.php",
		"getIpURL": "backend/getIP.php",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("serverListJSON()\n got: %v\nwant: %v", got, want)
	}
}
