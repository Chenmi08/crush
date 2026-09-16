package tools

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		in       string
		maxBytes int
		want     string
	}{
		{name: "shorter than limit", in: "hello", maxBytes: 10, want: "hello"},
		{name: "exact", in: "hello", maxBytes: 5, want: "hello"},
		{name: "ascii cut", in: "hello", maxBytes: 3, want: "hel"},
		{name: "zero", in: "hello", maxBytes: 0, want: ""},
		{name: "negative", in: "hello", maxBytes: -1, want: ""},
		{name: "splits multi-byte", in: "héllo", maxBytes: 2, want: "h"},
		{name: "multi-byte exact", in: "héllo", maxBytes: 3, want: "hé"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateUTF8(tc.in, tc.maxBytes)
			if got != tc.want {
				t.Fatalf("truncateUTF8(%q, %d) = %q, want %q", tc.in, tc.maxBytes, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8: %q", got)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		in       string
		maxRunes int
		want     string
	}{
		{name: "shorter than limit", in: "hello", maxRunes: 10, want: "hello"},
		{name: "exact", in: "hello", maxRunes: 5, want: "hello"},
		{name: "cut", in: "hello", maxRunes: 3, want: "hel"},
		{name: "multi-byte", in: "héllo", maxRunes: 3, want: "hél"},
		{name: "zero", in: "hello", maxRunes: 0, want: ""},
		{name: "empty", in: "", maxRunes: 5, want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := truncateRunes(tc.in, tc.maxRunes); got != tc.want {
				t.Fatalf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.maxRunes, got, tc.want)
			}
		})
	}
}
