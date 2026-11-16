package publisher_test

import (
	"context"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/publisher"
)

func TestRepoFile(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		signer  func(t *testing.T) publisher.MetadataSigner
		want    []string
		wantNot []string
	}{
		{"signed", "https://dl.example/", func(t *testing.T) publisher.MetadataSigner { return fakeSigner{t: t} }, []string{
			"[SUSE_SLFO_1.2]\n", "name=SUSE:SLFO:1.2\n", "baseurl=https://dl.example/" + stdPrefix + "\n", "gpgcheck=1\n",
			"gpgkey=https://dl.example/" + stdPrefix + "repodata/repomd.xml.key\n",
		}, nil},
		{"unsigned", "https://dl.example", func(*testing.T) publisher.MetadataSigner { return nil }, []string{"gpgcheck=0\n"}, []string{"gpgkey"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, publisher.Policy{Publish: true, RPMMD: true, Debuginfo: true})
			h.pub.DownloadURL = tt.url
			h.pub.Signer = tt.signer(t)
			h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"))
			h.publish(t)
			ops := h.store.TakeOps()
			raw, err := h.store.ReadObject(context.Background(), stdPrefix+"SUSE:SLFO:1.2.repo")
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(string(raw), w) {
					t.Errorf("repo file lacks %q:\n%s", w, raw)
				}
			}
			for _, w := range tt.wantNot {
				if strings.Contains(string(raw), w) {
					t.Errorf("repo file has %q:\n%s", w, raw)
				}
			}
			if indexOf(ops, "put:"+stdPrefix+"SUSE:SLFO:1.2.repo") > indexOf(ops, "put:"+stdPrefix+"repodata/repomd.xml") {
				t.Error(".repo uploaded after repomd.xml")
			}
		})
	}
}

func TestNoRepoFileWithoutDownloadURL(t *testing.T) {
	h := newHarness(t, publishAll)
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"))
	h.publish(t)
	if got := h.store.Keys(stdPrefix + "SUSE"); len(got) != 0 {
		t.Errorf(".repo file written without a download URL: %v", got)
	}
}
