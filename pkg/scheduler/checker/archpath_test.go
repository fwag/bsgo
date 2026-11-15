package checker

import "testing"

func TestArchpath(t *testing.T) {
	tests := []struct{ arch, host, want string }{
		{"x86_64", "", "x86_64:i686:i586:i486:i386:noarch"},
		{"i586", "", "i586:i486:i386:noarch"},
		{"i686", "", "i686:i586:i486:i386:noarch"},
		{"aarch64", "", "aarch64:aarch64_ilp32:armv8l:noarch"},
		{"riscv64", "", "riscv64:noarch"},
		{"i586", "x86_64", "i586:i486:i386:x86_64:noarch"},
		{"x86_64", "i586", "x86_64:i686:i586:i486:i386:noarch"},
		{"x86_64", "x86_64", "x86_64:i686:i586:i486:i386:noarch"},
	}
	for _, tt := range tests {
		if got := archpath(tt.arch, tt.host); got != tt.want {
			t.Errorf("archpath(%q, %q) = %q, want %q", tt.arch, tt.host, got, tt.want)
		}
	}
}
