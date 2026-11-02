package config

import "testing"

func TestParseSize(t *testing.T) {
	tests := []struct {
		in      string
		want    Size
		wantErr bool
	}{
		{"0", 0, false},
		{"1048576", MiB, false},
		{"512B", 512, false},
		{"1024KiB", MiB, false},
		{"1MiB", MiB, false},
		{" 2 GiB ", 2 * GiB, false},
		{"", 0, true},
		{"-1KiB", 0, true},
		{"1.5MiB", 0, true},
		{"1MB", 0, true},
		{"1kib", 0, true},
		{"99999999999999999999", 0, true},
		{"9999999999GiB", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseSize(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseSize(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseSize(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestSizeString(t *testing.T) {
	tests := []struct {
		in   Size
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{KiB, "1KiB"},
		{1536 * KiB, "1536KiB"},
		{MiB, "1MiB"},
		{3 * GiB, "3GiB"},
	}
	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("Size(%d).String() = %q, want %q", int64(tt.in), got, tt.want)
		}
	}
}
