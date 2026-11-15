//go:build oracle

// Package oracle compares the Go build configuration parser against the
// reference implementation, obs-build's Build::read_config (Perl). Perl is
// allowed here, and only here (ADR 0010, plan decision D2).
//
// Run:    go test -tags oracle ./test/oracle/...
// Regen:  go test -tags oracle ./test/oracle/... -update
//
// The Perl side normalizes the read_config hash into the JSON shape that
// buildconfig.Config marshals to. Perl-internal keys that are intentionally
// not modelled (none at the time of writing) would be listed here; the
// normalizer dies on any key it does not know, so a new obs-build key can not
// be silently dropped. Derived keys written by read_config as flat hash keys
// ("expandflags:foo", "buildflags:foo", "publishflags:foo") are regrouped into
// expandflags_h, buildflags_h and publishflags_h.
package oracle

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
)

var update = flag.Bool("update", false, "regenerate golden files from the Perl oracle")

const fixtureDir = "../fixtures/prjconf"

var arches = []string{"x86_64", "aarch64", "i586"}

const perlScript = `
use strict;
use warnings;
use JSON::PP;
use Build;

my ($arch, $file) = @ARGV;
my $c = Build::read_config($arch, $file) or die "read_config failed for $file\n";

my @scalars = qw(arch rawmacros release singleexport type buildengine binarytype
  cicntstart releaseprg releasesuffix target hostarch parse_error);
my @lists = qw(macros preinstall vminstall runscripts required support keep prefer
  ignore conflict ignorerebuild alsonative onlynative publishfilter repotype
  patterntype constraint expandflags buildflags publishflags repourl registryurl
  assetsurl modules);
my %maplist = map { $_ => 1 } qw(substitute fileprovides exportfilter conflicth);
my %mapbool = map { $_ => 1 } qw(order preferh ignoreh);
my %known = map { $_ => 1 } (@scalars, @lists, keys %maplist, keys %mapbool,
  qw(substitute_vers optflags release@));

my %o;
for my $k (@scalars) { my $v = $c->{$k}; $o{$k} = defined($v) ? "$v" : ''; }
for my $k (@lists) { $o{$k} = [ map { "$_" } @{$c->{$k} || []} ]; }
$o{'release@'} = [ map { "$_" } @{$c->{'release@'} || []} ];
for my $k (keys %maplist) {
  $o{$k} = { map { $_ => [ map { "$_" } @{$c->{$k}->{$_}} ] } keys %{$c->{$k}} };
}
for my $k (keys %mapbool) {
  $o{$k} = { map { $_ => JSON::PP::true } keys %{$c->{$k}} };
}
$o{'optflags'} = { map { $_ => "$c->{optflags}->{$_}" } keys %{$c->{optflags}} };
$o{'substitute_vers'} = {
  map { my $k = $_; $k => [ map { defined($_) ? "$_" : undef } @{$c->{substitute_vers}->{$k}} ] }
    keys %{$c->{substitute_vers}}
};
for my $f (qw(expandflags buildflags publishflags)) {
  $o{"${f}_h"} = {};
}
for my $k (keys %$c) {
  next if $known{$k};
  if ($k =~ /^(expandflags|buildflags|publishflags):(.*)$/s) {
    $o{"$1_h"}->{$2} = "$c->{$k}";
    next;
  }
  die "unmodelled read_config key: $k\n";
}
print JSON::PP->new->canonical->pretty->latin1->encode(\%o);
`

func fixtures(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, pat := range []string{"*.prjconf", "handmade/*.conf"} {
		m, err := filepath.Glob(filepath.Join(fixtureDir, pat))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Fatal("no fixtures found")
	}
	sort.Strings(files)
	return files
}

func goldenPath(file, arch string) string {
	base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	return filepath.Join(fixtureDir, "golden", base+"."+arch+".json")
}

func runOracle(t *testing.T, arch, file string) []byte {
	t.Helper()
	cmd := exec.Command("perl", "-I/usr/lib/build", "-MBuild", "-MJSON::PP", "-e", perlScript, arch, file)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("perl oracle failed: %v\n%s", err, stderr.String())
	}
	return stdout.Bytes()
}

func normalize(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func goJSON(t *testing.T, file, arch string) []byte {
	t.Helper()
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := buildconfig.Parse(string(text), arch)
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// diff lists the paths at which a and b differ.
func diff(path string, a, b any, out *[]string) {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, path+": type mismatch")
			return
		}
		for k, x := range av {
			y, ok := bv[k]
			if !ok {
				*out = append(*out, path+"/"+k+": missing in go")
				continue
			}
			diff(path+"/"+k, x, y, out)
		}
		for k := range bv {
			if _, ok := av[k]; !ok {
				*out = append(*out, path+"/"+k+": only in go")
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			*out = append(*out, fmt.Sprintf("%s: list differs (perl %d entries, go %d): perl=%v go=%v", path, len(av), len(bv), head(av), head(bv)))
			return
		}
		for i := range av {
			diff(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i], out)
		}
	default:
		if !reflect.DeepEqual(a, b) {
			*out = append(*out, fmt.Sprintf("%s: perl=%v go=%v", path, a, b))
		}
	}
}

func head(v any) any {
	if l, ok := v.([]any); ok && len(l) > 5 {
		return l[:5]
	}
	return v
}

func TestParserMatchesPerlOracle(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl not available")
	}
	for _, file := range fixtures(t) {
		for _, arch := range arches {
			t.Run(filepath.Base(file)+"/"+arch, func(t *testing.T) {
				perl := runOracle(t, arch, file)
				if *update {
					if err := os.WriteFile(goldenPath(file, arch), perl, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				var diffs []string
				diff("", normalize(t, perl), normalize(t, goJSON(t, file, arch)), &diffs)
				if len(diffs) > 0 {
					sort.Strings(diffs)
					if len(diffs) > 20 {
						diffs = diffs[:20]
					}
					t.Errorf("%d+ differences:\n%s", len(diffs), strings.Join(diffs, "\n"))
				}
			})
		}
	}
}
