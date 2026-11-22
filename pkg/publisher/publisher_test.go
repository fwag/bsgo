package publisher_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/publisher"
	"github.com/openSUSE/bsgo/pkg/scheduler/buildrepo"
)

type harness struct {
	store *publisher.MemoryStore
	idx   *fakeIndex
	cr    *fakeCreateRepo
	pub   *publisher.Publisher
	temp  string
}

func newHarness(t *testing.T, pol publisher.Policy) *harness {
	t.Helper()
	h := &harness{store: publisher.NewMemoryStore(), idx: &fakeIndex{}, cr: &fakeCreateRepo{}, temp: t.TempDir()}
	h.pub = &publisher.Publisher{
		Store: h.store, Index: h.idx, Policy: fixedPolicy(pol), CreateRepo: h.cr, TempRoot: h.temp,
	}
	return h
}

var publishAll = publisher.Policy{Publish: true, RPMMD: true}

func (h *harness) publish(t *testing.T) *publisher.Result {
	t.Helper()
	res, err := h.pub.Publish(context.Background(), testPrpa)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	return res
}

func (h *harness) assertTempEmpty(t *testing.T) {
	t.Helper()
	left, err := os.ReadDir(h.temp)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("publish left %d entries in its temp root", len(left))
	}
}

func TestPublishReplacesBinarySet(t *testing.T) {
	h := newHarness(t, publishAll)
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"), rpmSpec{name: "bash-doc", version: "5.2", release: "1.1", arch: "noarch"})
	h.idx.builds(t, h.store, testPrpa, "curl", rpmx("curl", "8.0"))
	h.publish(t)
	want := []string{"bash-5.2-1.1.x86_64.rpm", "bash-doc-5.2-1.1.noarch.rpm", "curl-8.0-1.1.x86_64.rpm"}
	if got := names(primaryOf(t, h.store, stdPrefix)); !reflect.DeepEqual(got, want) {
		t.Fatalf("first publish lists %v, want %v", got, want)
	}
	h.store.TakeOps()

	// bash is rebuilt: new version, and its noarch subpackage is gone.
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.3"))
	res := h.publish(t)

	want = []string{"bash-5.3-1.1.x86_64.rpm", "curl-8.0-1.1.x86_64.rpm"}
	if got := names(primaryOf(t, h.store, stdPrefix)); !reflect.DeepEqual(got, want) {
		t.Errorf("second publish lists %v, want %v", got, want)
	}
	for _, gone := range []string{"bash-5.2-1.1.x86_64.rpm", "bash-doc-5.2-1.1.noarch.rpm"} {
		if h.store.Has(stdPrefix + gone) {
			t.Errorf("%s is still published", gone)
		}
	}
	std := res.SubRepos[publisher.SubRepoStandard]
	if !reflect.DeepEqual(std.Transferred, []string{"bash-5.3-1.1.x86_64.rpm"}) {
		t.Errorf("transferred %v: only the rebuilt package may be copied", std.Transferred)
	}
	if !reflect.DeepEqual(std.Deleted, []string{"bash-5.2-1.1.x86_64.rpm", "bash-doc-5.2-1.1.noarch.rpm"}) {
		t.Errorf("deleted %v", std.Deleted)
	}
	h.assertTempEmpty(t)
}

func TestPublishIsIncrementalAndStateless(t *testing.T) {
	h := newHarness(t, publishAll)
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"))
	h.idx.builds(t, h.store, testPrpa, "curl", rpmx("curl", "8.0"))
	h.publish(t)
	if n := len(h.cr.calls); n != 1 || h.cr.calls[0].OldRepoDir != "" {
		t.Fatalf("first publish must generate from scratch, calls: %+v", h.cr.calls)
	}
	if d := h.store.TakeDownloads(); len(d) < 2 {
		t.Fatalf("first publish downloaded %v, want both RPMs", d)
	}

	h.idx.builds(t, h.store, testPrpa, "curl", rpmx("curl", "8.1"))
	h.publish(t)
	last := h.cr.calls[len(h.cr.calls)-1]
	if last.OldRepoDir == "" {
		t.Errorf("second publish did not reuse the published repodata")
	}
	var rpmDownloads []string
	for _, d := range h.store.TakeDownloads() {
		if strings.HasSuffix(d, ".rpm") {
			rpmDownloads = append(rpmDownloads, d)
		}
	}
	if len(rpmDownloads) != 1 || !strings.HasSuffix(rpmDownloads[0], "curl-8.1-1.1.x86_64.rpm") {
		t.Errorf("second publish downloaded %v, want only the new curl", rpmDownloads)
	}
	if _, err := os.Stat(last.OldRepoDir); !os.IsNotExist(err) {
		t.Errorf("old metadata dir survived the publish: %v", err)
	}
}

func TestPublishFallsBackWhenOldMetadataIsUnusable(t *testing.T) {
	h := newHarness(t, publishAll)
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"))
	h.publish(t)
	h.cr.failWithOld = true
	h.idx.builds(t, h.store, testPrpa, "curl", rpmx("curl", "8.0"))
	h.publish(t)
	want := []string{"bash-5.2-1.1.x86_64.rpm", "curl-8.0-1.1.x86_64.rpm"}
	if got := names(primaryOf(t, h.store, stdPrefix)); !reflect.DeepEqual(got, want) {
		t.Errorf("lists %v, want %v", got, want)
	}
}

func TestPublishDebugPolicy(t *testing.T) {
	bash := []rpmSpec{
		rpmx("bash", "5.2"),
		rpmx("bash-debuginfo", "5.2"),
		rpmx("bash-debugsource", "5.2"),
		{name: "bash", version: "5.2", release: "1.1", arch: "src"},
	}
	tests := []struct {
		name      string
		debuginfo bool
		std       []string
		debug     []string
	}{
		{"debuginfo off drops debug and source packages", false, []string{"bash-5.2-1.1.x86_64.rpm"}, nil},
		{"debuginfo on adds a debug sub-repository", true, []string{"bash-5.2-1.1.x86_64.rpm"},
			[]string{"bash-debuginfo-5.2-1.1.x86_64.rpm", "bash-debugsource-5.2-1.1.x86_64.rpm"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, publisher.Policy{Publish: true, RPMMD: true, Debuginfo: tt.debuginfo})
			h.idx.builds(t, h.store, testPrpa, "bash", bash...)
			h.publish(t)
			if got := names(primaryOf(t, h.store, stdPrefix)); !reflect.DeepEqual(got, tt.std) {
				t.Errorf("standard lists %v, want %v", got, tt.std)
			}
			if tt.debug == nil {
				if keys := h.store.Keys(debugPrefix); len(keys) != 0 {
					t.Errorf("debug sub-repository written: %v", keys)
				}
			} else if got := names(primaryOf(t, h.store, debugPrefix)); !reflect.DeepEqual(got, tt.debug) {
				t.Errorf("debug lists %v, want %v", got, tt.debug)
			}
			for _, k := range h.store.Keys("repos/") {
				if strings.Contains(k, ".src.rpm") {
					t.Errorf("source package published: %s", k)
				}
			}
		})
	}
}

func TestPublishSkips(t *testing.T) {
	tests := []struct {
		name   string
		pol    publisher.Policy
		build  bool
		reason string
	}{
		{"publish flag off", publisher.Policy{RPMMD: true}, true, "publishing disabled"},
		{"no rpm-md repo type", publisher.Policy{Publish: true}, true, "publishing disabled"},
		{"empty build repository", publishAll, false, "build repository is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.pol)
			if tt.build {
				h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"))
			}
			before := len(h.store.Keys(""))
			res := h.publish(t)
			if res.Skipped != tt.reason {
				t.Errorf("skipped = %q, want %q", res.Skipped, tt.reason)
			}
			if after := len(h.store.Keys("")); after != before || len(h.cr.calls) != 0 {
				t.Errorf("a skipped publish wrote objects or ran createrepo (%d -> %d, %d runs)", before, after, len(h.cr.calls))
			}
		})
	}
}

type unknownPolicy struct{}

func (unknownPolicy) Policy(context.Context, buildrepo.Prpa) (publisher.Policy, error) {
	return publisher.Policy{}, publisher.ErrUnknownTarget
}

func TestPublishUnknownTargetAndBadPrpa(t *testing.T) {
	h := newHarness(t, publishAll)
	h.pub.Policy = unknownPolicy{}
	if res := h.publish(t); res.Skipped == "" {
		t.Error("unknown target must be skipped")
	}
	if _, err := h.pub.Publish(context.Background(), buildrepo.Prpa{Project: "p", Repo: "..", Arch: "x"}); err == nil {
		t.Error("path traversal in the prpa must be rejected")
	}
}

func TestPublishExportFilter(t *testing.T) {
	tests := []struct {
		name   string
		filter map[string][]string
		want   []string
	}{
		{"no filter publishes everything", nil, []string{"a-1-1.1.i586.rpm", "b-1-1.1.x86_64.rpm", "c-1-1.1.noarch.rpm"}},
		{"i586 exported to i586 only stays out of x86_64", map[string][]string{`\.i586\.rpm$`: {"i586"}},
			[]string{"b-1-1.1.x86_64.rpm", "c-1-1.1.noarch.rpm"}},
		{"rule listing the own arch keeps the binary", map[string][]string{`\.i586\.rpm$`: {"i586", "x86_64"}},
			[]string{"a-1-1.1.i586.rpm", "b-1-1.1.x86_64.rpm", "c-1-1.1.noarch.rpm"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, publisher.Policy{Publish: true, RPMMD: true, ExportFilter: tt.filter})
			h.idx.builds(t, h.store, testPrpa, "u",
				rpmSpec{name: "a", version: "1", release: "1.1", arch: "i586"},
				rpmx("b", "1"),
				rpmSpec{name: "c", version: "1", release: "1.1", arch: "noarch"})
			h.publish(t)
			if got := names(primaryOf(t, h.store, stdPrefix)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("lists %v, want %v", got, tt.want)
			}
		})
	}
	h := newHarness(t, publisher.Policy{Publish: true, RPMMD: true, ExportFilter: map[string][]string{"(": {"x"}}})
	h.idx.builds(t, h.store, testPrpa, "u", rpmx("b", "1"))
	if _, err := h.pub.Publish(context.Background(), testPrpa); err == nil {
		t.Error("an invalid ExportFilter expression must fail the publish")
	}
}

func TestRepomdIsUploadedLastAndStaleObjectsGoAfterIt(t *testing.T) {
	h := newHarness(t, publisher.Policy{Publish: true, RPMMD: true, Debuginfo: true})
	h.pub.Signer = fakeSigner{t: t}
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"), rpmx("bash-debuginfo", "5.2"))
	h.publish(t)
	h.store.TakeOps()

	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.3"), rpmx("bash-debuginfo", "5.3"))
	h.publish(t)
	ops := h.store.TakeOps()

	for _, prefix := range []string{stdPrefix, debugPrefix} {
		repomd := indexOf(ops, "put:"+prefix+"repodata/repomd.xml")
		if repomd < 0 {
			t.Fatalf("%s: repomd.xml not uploaded: %v", prefix, ops)
		}
		for i, op := range ops {
			inRepo := strings.Contains(op, prefix)
			isRPMCopy := strings.HasPrefix(op, "copy:") && strings.HasSuffix(op, ".rpm")
			isRPMDelete := strings.HasPrefix(op, "del:") && strings.HasSuffix(op, ".rpm")
			switch {
			case !inRepo:
			case isRPMCopy && i > repomd:
				t.Errorf("%s: RPM copy %q after repomd.xml", prefix, op)
			case isRPMDelete && i < repomd:
				t.Errorf("%s: stale RPM deleted before the repomd.xml switch: %q", prefix, op)
			case strings.HasPrefix(op, "put:") && !strings.HasSuffix(op, "repomd.xml") && i > repomd:
				t.Errorf("%s: metadata upload %q after repomd.xml", prefix, op)
			}
		}
		if indexOf(ops, "put:"+prefix+"repodata/repomd.xml.asc") > repomd {
			t.Errorf("%s: signature uploaded after repomd.xml", prefix)
		}
		if indexOf(ops, "del:"+prefix+"bash-5.2-1.1.x86_64.rpm") < 0 && indexOf(ops, "del:"+prefix+"bash-debuginfo-5.2-1.1.x86_64.rpm") < 0 {
			t.Errorf("%s: old NEVR not deleted: %v", prefix, ops)
		}
	}
	if !h.store.Has(stdPrefix + "repodata/repomd.xml.key") {
		t.Error("public key not published")
	}
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

type fakeSigner struct{ t *testing.T }

func (s fakeSigner) SignRepositoryMetadata(_ context.Context, dir string, _ publisher.SignOptions) (string, string, error) {
	raw, err := os.ReadFile(dir + "/repomd.xml")
	if err != nil {
		s.t.Errorf("signer called without repomd.xml: %v", err)
	}
	sig, key := s.t.TempDir()+"/repomd.xml.asc", s.t.TempDir()+"/repomd.xml.key"
	if err := os.WriteFile(sig, []byte("sig of "+string(raw)), 0o600); err != nil {
		return "", "", err
	}
	return sig, key, os.WriteFile(key, []byte("key"), 0o600)
}

func TestFailedPublishSwitchesNothingAndCleansUp(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(h *harness)
	}{
		{"createrepo fails", func(h *harness) { h.cr.err = errors.New("boom") }},
		{"artifact does not match the index", func(h *harness) {
			h.store.Set("artifacts/SUSE:SLFO:1.2/standard/x86_64/bash/bash-5.2-1.1.x86_64.rpm", []byte("tampered"))
		}},
		{"signer fails", func(h *harness) { h.pub.Signer = failingSigner{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, publishAll)
			h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"))
			tt.break_(h)
			if _, err := h.pub.Publish(context.Background(), testPrpa); err == nil {
				t.Fatal("publish must fail")
			}
			if h.store.Has(stdPrefix + "repodata/repomd.xml") {
				t.Error("repomd.xml was switched by a failed publish")
			}
			h.assertTempEmpty(t)
		})
	}
}

type failingSigner struct{}

func (failingSigner) SignRepositoryMetadata(context.Context, string, publisher.SignOptions) (string, string, error) {
	return "", "", errors.New("signd unavailable")
}

type recordingModify struct{ types []string }

func (m *recordingModify) Run(_ context.Context, opts publisher.ModifyRepoOptions) error {
	m.types = append(m.types, opts.MDType)
	return os.WriteFile(opts.RepodataDir+"/hash-"+opts.MDType+".xml.gz", []byte(opts.MDType), 0o600)
}

func TestPublishEnrichesStandardRepository(t *testing.T) {
	h := newHarness(t, publisher.Policy{Publish: true, RPMMD: true, Debuginfo: true})
	mr := &recordingModify{}
	h.pub.ModifyRepo = mr
	h.pub.Enrich = func(context.Context, buildrepo.Prpa) (publisher.Enrichment, error) {
		return publisher.Enrichment{
			Advisories:    []v1.Advisory{{ID: "SUSE-2026-1", Title: "fix", Type: "security"}},
			AppStreamData: []byte("<components/>"),
		}, nil
	}
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"), rpmx("bash-debuginfo", "5.2"))
	h.publish(t)
	if !reflect.DeepEqual(mr.types, []string{"updateinfo", "appstream"}) {
		t.Errorf("modifyrepo ran for %v, want updateinfo and appstream once, on the standard repository", mr.types)
	}
	if !h.store.Has(stdPrefix+"repodata/hash-updateinfo.xml.gz") || !h.store.Has(stdPrefix+"repodata/hash-appstream.xml.gz") {
		t.Errorf("enriched metadata not uploaded: %v", h.store.Keys(stdPrefix+"repodata/"))
	}
	if h.store.Has(debugPrefix + "repodata/hash-updateinfo.xml.gz") {
		t.Error("the debug repository must not carry updateinfo")
	}
}

type captureEvents struct{ subjects []string }

func (c *captureEvents) PublishEvent(_ context.Context, subject string, _ []byte) error {
	c.subjects = append(c.subjects, subject)
	return nil
}

func TestPublishAnnouncesAndGarbageCollects(t *testing.T) {
	h := newHarness(t, publishAll)
	ev := &captureEvents{}
	h.pub.Events = ev
	h.pub.GCGrace = time.Hour
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"))
	h.publish(t)
	old := h.store.Keys(stdPrefix + "repodata/")
	for _, k := range old {
		h.store.SetModTime(k, time.Now().Add(-2*time.Hour))
	}
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.3"))
	h.publish(t)
	if len(ev.subjects) != 2 || ev.subjects[0] != "bsgo.events.published" {
		t.Errorf("events %v", ev.subjects)
	}
	if h.store.Has(old[0]) && !strings.HasSuffix(old[0], "repomd.xml") {
		t.Errorf("superseded metadata %s survived garbage collection", old[0])
	}
}

func TestPublishAfterBuildWithoutBinaries(t *testing.T) {
	h := newHarness(t, publishAll)
	h.idx.builds(t, h.store, testPrpa, "bash", rpmx("bash", "5.2"))
	h.idx.builds(t, h.store, testPrpa, "curl", rpmx("curl", "8.0"))
	h.publish(t)

	// curl is rebuilt by a flavor that only runs tests: no binaries remain.
	h.idx.builds(t, h.store, testPrpa, "curl")
	res := h.publish(t)
	if got, want := names(primaryOf(t, h.store, stdPrefix)), []string{"bash-5.2-1.1.x86_64.rpm"}; !reflect.DeepEqual(got, want) {
		t.Errorf("publish lists %v, want %v", got, want)
	}
	if h.store.Has(stdPrefix + "curl-8.0-1.1.x86_64.rpm") {
		t.Error("the RPM of the emptied unit is still published")
	}
	if res.SubRepos[publisher.SubRepoStandard].Deleted == nil {
		t.Error("the emptied unit's RPM was not reported as deleted")
	}
	h.assertTempEmpty(t)
}

func TestPublishSameFileNameFromSeveralUnits(t *testing.T) {
	same := rpmSpec{name: "kernel-livepatch", version: "1", release: "1.1", arch: "x86_64"}
	old, newer := same, same
	old.content, newer.content = "from the first unit", "from the second unit"
	tests := []struct {
		name      string
		units     map[string]rpmSpec
		wantBody  string
		conflicts int
	}{
		{"identical copies are no conflict", map[string]rpmSpec{"a": same, "b": same}, "rpm:" + same.filename(), 0},
		{"different content: last unit wins", map[string]rpmSpec{"a": old, "b": newer}, "from the second unit", 1},
		{"three units, last wins", map[string]rpmSpec{"a": old, "b": newer, "c": old}, "from the first unit", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, publishAll)
			for _, u := range []string{"c", "b", "a"} { // listing order must not matter
				if spec, ok := tt.units[u]; ok {
					h.idx.builds(t, h.store, testPrpa, u, spec)
				}
			}
			res := h.publish(t)
			if got := len(res.Conflicts); got != tt.conflicts {
				t.Fatalf("%d conflicts, want %d: %+v", got, tt.conflicts, res.Conflicts)
			}
			if res.SubRepos[publisher.SubRepoStandard].Packages != 1 {
				t.Errorf("want exactly one published binary: %+v", res.SubRepos)
			}
			key := stdPrefix + same.filename()
			raw, err := h.store.ReadObject(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != tt.wantBody {
				t.Errorf("published %q, want %q", raw, tt.wantBody)
			}
			if tt.conflicts == 1 && !strings.HasSuffix(res.Conflicts[0].Kept, same.filename()) {
				t.Errorf("conflict does not name the kept binary: %+v", res.Conflicts[0])
			}
		})
	}
}
