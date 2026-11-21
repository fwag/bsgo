package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfirmDeletion(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"y\n", true}, {"YES\n", true}, {"n\n", false}, {"\n", false}, {"", false}, {"maybe\n", false},
	}
	for _, tt := range tests {
		var w bytes.Buffer
		got, err := confirmDeletion(strings.NewReader(tt.input), &w)
		if err != nil || got != tt.want {
			t.Errorf("input %q: got %v, %v want %v", tt.input, got, err, tt.want)
		}
	}
}
