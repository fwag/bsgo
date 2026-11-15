package publisher_test

import (
	"testing"

	"github.com/openSUSE/bsgo/pkg/publisher"
)

func TestClassifyRPM(t *testing.T) {
	cases := []struct {
		filename string
		expected publisher.SubRepoType
	}{
		{"curl-8.1.0-1.x86_64.rpm", publisher.SubRepoStandard},
		{"python3-doc-3.11.0-1.noarch.rpm", publisher.SubRepoStandard},
		{"ripgrep-14.1.0-1.aarch64.rpm", publisher.SubRepoStandard},
		{"curl-8.1.0-1.src.rpm", publisher.SubRepoSource},
		{"kernel-source-6.4.0-1.nosrc.rpm", publisher.SubRepoSource},
		{"curl-debuginfo-8.1.0-1.x86_64.rpm", publisher.SubRepoDebug},
		{"curl-debugsource-8.1.0-1.x86_64.rpm", publisher.SubRepoDebug},
		{"glibc-debuginfo-32bit-2.38-1.x86_64.rpm", publisher.SubRepoDebug},
		{"test-debuginfo.rpm", publisher.SubRepoDebug},
		{"test-debugsource.rpm", publisher.SubRepoDebug},
		{"/path/to/my-pkg-debuginfo-1.0.rpm", publisher.SubRepoDebug},
		{"/path/to/my-pkg-1.0.src.rpm", publisher.SubRepoSource},
		{"/path/to/my-pkg-1.0.x86_64.rpm", publisher.SubRepoStandard},
	}

	for _, tc := range cases {
		actual := publisher.ClassifyRPM(tc.filename)
		if actual != tc.expected {
			t.Errorf("ClassifyRPM(%q) = %q, expected %q", tc.filename, actual, tc.expected)
		}
	}
}

func TestSubRepoS3Prefix(t *testing.T) {
	prj := "openSUSE:Factory"
	target := "standard"
	arch := "x86_64"

	if p := publisher.SubRepoS3Prefix(prj, target, arch, publisher.SubRepoStandard); p != "repos/openSUSE:Factory/standard/x86_64/" {
		t.Errorf("unexpected standard S3 prefix: %q", p)
	}
	if p := publisher.SubRepoS3Prefix(prj, target, arch, publisher.SubRepoDebug); p != "repos/openSUSE:Factory/standard/debug/x86_64/" {
		t.Errorf("unexpected debug S3 prefix: %q", p)
	}
	if p := publisher.SubRepoS3Prefix(prj, target, arch, publisher.SubRepoSource); p != "repos/openSUSE:Factory/standard/src/" {
		t.Errorf("unexpected source S3 prefix: %q", p)
	}
}
