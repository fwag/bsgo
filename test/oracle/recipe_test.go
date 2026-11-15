//go:build oracle

package oracle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
	"github.com/openSUSE/bsgo/pkg/recipe"
	"github.com/openSUSE/bsgo/pkg/recipe/recipetest"
)

const (
	recipeProject  = "SUSE:SLFO:1.2"
	recipeRepo     = "standard"
	recipeArch     = "x86_64"
	recipePrjconf  = "../fixtures/prjconf/SUSE_SLFO_1.2.prjconf"
	recipeSpecDir  = "../fixtures/specs"
	recipeGolden   = "../fixtures/specs/golden.json"
	defaultCorpus  = "/tmp/bsgo-fixtures/specs"
	corpusEnv      = "BSGO_SPEC_CORPUS"
	minParityRatio = 0.99
)

// recipePerlScript parses every unit listed in the job file with the config
// text in the first argument, the way bs_srcserver does: buildflavor and
// obspackage are set in the config before Build::parse_typed.
const recipePerlScript = `
use strict;
use warnings;
use JSON::PP;
use Build;

my ($arch, $cfgfile, $jobfile) = @ARGV;
my $fh;
my $slurp = sub { local $/; open($fh, '<', $_[0]) or die "$_[0]: $!"; my $d = <$fh>; close $fh; return $d; };
my @lines = split("\n", $slurp->($cfgfile));
my $c = Build::read_config($arch, \@lines) or die "read_config failed\n";
my $jobs = JSON::PP->new->decode($slurp->($jobfile));

sub list { return [ map { "$_" } @{$_[0] || []} ]; }

my @res;
for my $j (@$jobs) {
  local $c->{'buildflavor'} = $j->{flavor} ne '' ? $j->{flavor} : undef;
  local $c->{'obspackage'} = $j->{package};
  my $d;
  eval { $d = Build::parse_typed($c, $j->{path}, 'spec'); };
  my $r = { name => '', version => '', release => '', epoch => '', deps => [],
    subpacks => [], exclarch => [], badarch => [], buildtarget => '',
    constraints => [], error => '' };
  if ($@) {
    ($r->{error} = $@) =~ s/\n.*//s;
  } elsif (!$d) {
    $r->{error} = "can not parse $j->{file}";
  } elsif ($d->{error}) {
    $r->{error} = "$d->{error}";
  } else {
    $r->{$_} = defined($d->{$_}) ? "$d->{$_}" : '' for qw(name version release epoch buildtarget);
    $r->{deps} = list($d->{deps});
    $r->{subpacks} = list($d->{subpacks});
    $r->{exclarch} = list($d->{exclarch});
    $r->{badarch} = list($d->{badarch});
    $r->{constraints} = list($d->{buildconstraint});
    $r->{error} = "can not parse name from $j->{file}" unless defined $d->{name};
  }
  push @res, $r;
}
print JSON::PP->new->canonical->latin1->encode(\@res);
`

type perlJob struct {
	Path    string `json:"path"`
	File    string `json:"file"`
	Package string `json:"package"`
	Flavor  string `json:"flavor"`
}

func runRecipePerl(t *testing.T, cfgText string, cases []recipetest.Case) []recipetest.Result {
	t.Helper()
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config")
	if err := os.WriteFile(cfgFile, []byte(cfgText), 0o600); err != nil {
		t.Fatal(err)
	}
	jobs := make([]perlJob, 0, len(cases))
	for _, c := range cases {
		jobs = append(jobs, perlJob{Path: c.Path, File: c.RecipeFile, Package: c.Package, Flavor: c.Flavor})
	}
	jb, err := json.Marshal(jobs)
	if err != nil {
		t.Fatal(err)
	}
	jobFile := filepath.Join(dir, "jobs.json")
	if err := os.WriteFile(jobFile, jb, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("perl", "-I/usr/lib/build", "-MBuild", "-MJSON::PP", "-e", recipePerlScript, recipeArch, cfgFile, jobFile)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("perl oracle failed: %v\n%s", err, stderr.String())
	}
	var res []recipetest.Result
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode perl output: %v", err)
	}
	if len(res) != len(cases) {
		t.Fatalf("perl returned %d results for %d units", len(res), len(cases))
	}
	return res
}

func goRecipeResult(t *testing.T, cfg *buildconfig.Config, c recipetest.Case) recipetest.Result {
	t.Helper()
	data, err := os.ReadFile(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := recipe.Parse(cfg, filepath.Base(c.Path), data, recipe.Options{Package: c.Package, Flavor: c.Flavor})
	if err != nil {
		return recipetest.Result{Error: err.Error()}
	}
	return recipetest.FromInfo(info)
}

// compareUnits runs Go and Perl over cases and returns the per-unit diffs.
func compareUnits(t *testing.T, cases []recipetest.Case) (diffs []string, identical int) {
	t.Helper()
	cfg, text, err := recipetest.LoadConfig(recipePrjconf, recipeProject, recipeRepo, recipeArch)
	if err != nil {
		t.Fatal(err)
	}
	var usable []recipetest.Case
	for _, c := range cases {
		if c.Path != "" {
			usable = append(usable, c)
		}
	}
	perl := runRecipePerl(t, text, usable)
	for i, c := range usable {
		got := goRecipeResult(t, cfg, c)
		if reflect.DeepEqual(got, perl[i]) {
			identical++
			continue
		}
		diffs = append(diffs, describeDiff(c, perl[i], got))
	}
	return diffs, identical
}

func describeDiff(c recipetest.Case, perl, got recipetest.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s):", c.Name, c.RecipeFile)
	pv, gv := reflect.ValueOf(perl), reflect.ValueOf(got)
	for i := 0; i < pv.NumField(); i++ {
		if !reflect.DeepEqual(pv.Field(i).Interface(), gv.Field(i).Interface()) {
			fmt.Fprintf(&b, "\n  %s: perl=%v go=%v", pv.Type().Field(i).Name, pv.Field(i).Interface(), gv.Field(i).Interface())
		}
	}
	return b.String()
}

func requirePerl(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl not available")
	}
}

// TestRecipeParserMatchesPerlOracle runs the full spec corpus. Parity must be
// at least 99% over all units; every diff is logged and must be listed in
// known_diffs.md.
func TestRecipeParserMatchesPerlOracle(t *testing.T) {
	requirePerl(t)
	dir := os.Getenv(corpusEnv)
	if dir == "" {
		dir = defaultCorpus
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("spec corpus %s not available", dir)
	}
	cases, err := recipetest.Cases(dir, recipeRepo)
	if err != nil {
		t.Fatal(err)
	}
	diffs, identical := compareUnits(t, cases)
	total := identical + len(diffs)
	ratio := float64(identical) / float64(total)
	t.Logf("recipe parity: %d/%d units identical (%.2f%%), %d diffs", identical, total, ratio*100, len(diffs))
	for _, d := range diffs {
		t.Log(d)
	}
	if ratio < minParityRatio {
		t.Errorf("parity %.2f%% below %.0f%%", ratio*100, minParityRatio*100)
	}
}

// TestRecipeGoldenMatchesPerlOracle regenerates (-update) or verifies the
// golden file of the curated corpus against Perl.
func TestRecipeGoldenMatchesPerlOracle(t *testing.T) {
	requirePerl(t)
	cases, err := recipetest.Cases(recipeSpecDir, recipeRepo)
	if err != nil {
		t.Fatal(err)
	}
	_, text, err := recipetest.LoadConfig(recipePrjconf, recipeProject, recipeRepo, recipeArch)
	if err != nil {
		t.Fatal(err)
	}
	var usable []recipetest.Case
	for _, c := range cases {
		if c.Path != "" {
			usable = append(usable, c)
		}
	}
	perl := runRecipePerl(t, text, usable)
	golden := map[string]recipetest.Result{}
	for i, c := range usable {
		golden[c.Name] = perl[i]
	}
	b, err := json.MarshalIndent(golden, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	if *update {
		if err := os.WriteFile(recipeGolden, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(recipeGolden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, b) {
		t.Errorf("%s is stale; run with -update", recipeGolden)
	}
}
