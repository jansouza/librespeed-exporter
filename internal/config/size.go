package config

import (
	"fmt"
	"regexp"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// Size is an amount of data in bytes. In YAML it is written either as a plain
// integer number of bytes or as a number followed by a unit: B, KiB, MiB or
// GiB.
type Size int64

// Binary size units.
const (
	B   Size = 1
	KiB      = 1024 * B
	MiB      = 1024 * KiB
	GiB      = 1024 * MiB
)

var sizeUnits = map[string]Size{"": B, "B": B, "KiB": KiB, "MiB": MiB, "GiB": GiB}

var sizePattern = regexp.MustCompile(`^\s*(\d+)\s*([A-Za-z]*)\s*$`)

// ParseSize parses a size such as "1024KiB" or "1MiB".
func ParseSize(s string) (Size, error) {
	m := sizePattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	unit, ok := sizeUnits[m[2]]
	if !ok {
		return 0, fmt.Errorf("invalid size %q: unknown unit %q, use B, KiB, MiB or GiB", s, m[2])
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n > int64((1<<63-1)/unit) {
		return 0, fmt.Errorf("invalid size %q: out of range", s)
	}
	return Size(n) * unit, nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *Size) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: size must be a scalar", node.Line)
	}
	v, err := ParseSize(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*s = v
	return nil
}

// String formats the size with the largest unit that divides it exactly.
func (s Size) String() string {
	for _, u := range []struct {
		name string
		size Size
	}{{"GiB", GiB}, {"MiB", MiB}, {"KiB", KiB}} {
		if s != 0 && s%u.size == 0 {
			return strconv.FormatInt(int64(s/u.size), 10) + u.name
		}
	}
	return strconv.FormatInt(int64(s), 10) + "B"
}
