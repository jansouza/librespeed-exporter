package clientinfo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jansouza/librespeed-exporter/internal/config"
)

func TestParse(t *testing.T) {
	// Response shapes recorded from public LibreSpeed PHP backends, with
	// documentation addresses.
	tests := []struct {
		name string
		body string
		want Info
	}{
		{
			name: "ISP looked up",
			body: `{"processedString":"203.0.113.7 - TELEFÔNICA BRASIL S.A, BR (510 km)","rawIspInfo":{"ip":"203.0.113.7","city":"Salvador","country":"BR","org":"AS26599 TELEFÔNICA BRASIL S.A","readme":"https:\/\/ipinfo.io\/missingauth"}}`,
			want: Info{IP: "203.0.113.7", ISP: "AS26599 TELEFÔNICA BRASIL S.A"},
		},
		{
			name: "ISP not looked up",
			body: `{"processedString":"203.0.113.8 - Unknown ISP","rawIspInfo":""}`,
			want: Info{IP: "203.0.113.8"},
		},
		{
			name: "ipinfo.io answered without org",
			body: `{"processedString":"203.0.113.9 - Unknown ISP","rawIspInfo":{"ip":"203.0.113.9"}}`,
			want: Info{IP: "203.0.113.9"},
		},
		{
			name: "plain text from an older backend",
			body: "203.0.113.10 - Unknown ISP\n",
			want: Info{IP: "203.0.113.10"},
		},
		{
			name: "IPv6",
			body: `{"processedString":"2001:db8::1 - Unknown ISP","rawIspInfo":""}`,
			want: Info{IP: "2001:db8::1"},
		},
		{
			name: "IPv4-mapped IPv6",
			body: `{"processedString":"::ffff:203.0.113.11 - Unknown ISP","rawIspInfo":""}`,
			want: Info{IP: "203.0.113.11"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parse([]byte(tt.body))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseRejectsNonPublicAddresses(t *testing.T) {
	// The PHP backend labels these "private IPv4 access", "localhost IPv6
	// access" and so on.
	for _, ip := range []string{"10.0.0.5", "192.168.1.20", "127.0.0.1", "::1", "fe80::1", "fd00::1"} {
		t.Run(ip, func(t *testing.T) {
			_, err := parse([]byte(`{"processedString":"` + ip + ` - private IPv4 access","rawIspInfo":""}`))
			if !errors.Is(err, ErrNotPublic) {
				t.Errorf("got %v, want ErrNotPublic", err)
			}
		})
	}
}

func TestParseRejectsResponsesWithoutIP(t *testing.T) {
	for _, body := range []string{``, `{}`, `{"processedString":"","rawIspInfo":""}`, `<html>Not Found</html>`} {
		_, err := parse([]byte(body))
		if err == nil || errors.Is(err, ErrNotPublic) {
			t.Errorf("parse(%q) = %v, want an error", body, err)
		}
	}
}

func TestFetch(t *testing.T) {
	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"processedString":"203.0.113.8 - Unknown ISP","rawIspInfo":""}`))
	}))
	defer ts.Close()

	srv := config.Server{Name: "a", URL: ts.URL + "/speedtest/", IPPath: "backend/getIP.php"}
	got, err := Fetch(context.Background(), srv, config.Default().Test)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got != (Info{IP: "203.0.113.8"}) {
		t.Errorf("got %+v", got)
	}
	if gotPath != "/speedtest/backend/getIP.php" || gotQuery != "isp=true" {
		t.Errorf("requested %s?%s", gotPath, gotQuery)
	}
}

func TestFetchFailsOnNon200(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()

	srv := config.Server{Name: "a", URL: ts.URL, IPPath: "backend/getIP.php"}
	_, err := Fetch(context.Background(), srv, config.Default().Test)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("got %v, want a 404 error", err)
	}
}
