package checker

import (
	"slices"
	"strings"
	"testing"
)

func TestAddMeta(t *testing.T) {
	tests := []struct {
		name     string
		producer []string
		bin      string
		packid   string
		want     []string
	}{
		{
			name:     "producer meta is nested under the binary",
			producer: []string{"s1  prod", "i1  dep", "i2  dep/sub"},
			bin:      "libfoo", packid: "me",
			want: []string{"s1  libfoo", "i1  libfoo/dep", "i2  libfoo/dep/sub"},
		},
		{
			name:     "our own build results never count",
			producer: []string{"s1  me", "i1  dep"},
			bin:      "libme", packid: "me",
			want: nil,
		},
		{
			name:     "a package that merely ends like us is not us",
			producer: []string{"s1  prime"},
			bin:      "libprime", packid: "me",
			want: []string{"s1  libprime"},
		},
		{
			name:     "empty meta adds nothing",
			producer: nil,
			bin:      "x", packid: "me",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := addMeta(nil, tc.producer, tc.bin, tc.packid)
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGenMeta(t *testing.T) {
	tests := []struct {
		name    string
		subpack []string
		algo    int
		deps    []string
		want    []string
	}{
		{
			name:    "sorted by depth, duplicates keep the shallowest",
			subpack: []string{"me"},
			algo:    1,
			deps: []string{
				"i2  b/z", "i1  b", "i3  a", "i2  z", "i2  a/z",
			},
			want: []string{"i3  a", "i1  b", "i2  z"},
		},
		{
			name:    "lines through our own binaries are cut",
			subpack: []string{"me", "me-devel"},
			algo:    1,
			deps: []string{
				"s2  other", "s2  other/me", "s9  other/me/deep", "s3  other/x", "s4  other/me-devel/y",
			},
			want: []string{"s2  other", "s3  other/x"},
		},
		{
			name:    "our own binaries as direct dependencies are not a cycle",
			subpack: []string{"me", "me-devel"},
			algo:    1,
			deps:    []string{"s1  me-devel", "s2  other", "s3  me-devel/dep"},
			want:    []string{"s2  other"},
		},
		{
			name:    "algo 0 drops every line that goes through a member that depends on us",
			subpack: []string{"me"},
			algo:    0,
			deps: []string{
				"s1  peer", "s2  peer/me", "s4  third", "s3  third/peer/dep", "s3  q/r/s/dep",
			},
			want: []string{"s1  peer", "s4  third", "s3  q/r/s/dep"},
		},
		{
			name:    "algo 1 also drops the deeper copy of a line seen through the cycle",
			subpack: []string{"me"},
			algo:    1,
			deps: []string{
				"s1  peer", "s2  peer/me", "s4  third", "s3  third/peer/dep", "s3  q/r/s/dep",
			},
			want: []string{"s1  peer", "s4  third"},
		},
		{
			name:    "no subpackages, nothing to cut",
			subpack: nil,
			algo:    1,
			deps:    []string{"i1  a/me", "i2  a"},
			want:    []string{"i2  a", "i1  a/me"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := genMeta(tc.subpack, tc.deps, tc.algo)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGenMetaRejectsBadLines(t *testing.T) {
	for _, l := range []string{"nospace", "  lead", "a b  c"} {
		if _, err := genMeta([]string{"me"}, []string{l}, 1); err == nil {
			t.Errorf("line %q accepted", l)
		}
	}
}

func TestDiffDepth(t *testing.T) {
	tests := []struct {
		name   string
		m1, m2 string
		want   int
	}{
		{"same", "s  p|a  x", "s  p|a  x", 0},
		{"source differs", "s1  p|a  x", "s2  p|a  x", 1},
		{"top level dependency differs", "s  p|a  x", "s  p|b  x", 2},
		{"nested dependency differs", "s  p|a  x|b  x/y", "s  p|a  x|c  x/y", 3},
		{"the shallower line wins", "s  p|a  x/y/z", "s  p|a  x/y", 3},
		{"new top level dependency", "s  p|a  x|b  y", "s  p|a  x", 2},
		{"new nested dependency in the shorter meta", "s  p|a  x", "s  p|a  x|b  x/y", 3},
		{"empty old meta", "s  p", "", 1},
		{"both empty", "", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			split := func(s string) []string {
				if s == "" {
					return nil
				}
				return strings.Split(s, "|")
			}
			if got := diffDepth(split(tc.m1), split(tc.m2)); got != tc.want {
				t.Errorf("diffDepth = %d, want %d", got, tc.want)
			}
			if got := diffDepth(split(tc.m2), split(tc.m1)); got != tc.want {
				t.Errorf("reversed diffDepth = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestShortID(t *testing.T) {
	if got := shortID(strings.Repeat("ab", 32)); len(got) != metaIDLen {
		t.Errorf("long id has %d digits", len(got))
	}
	if got := shortID("abc"); got != "abc" {
		t.Errorf("short id changed to %q", got)
	}
}
