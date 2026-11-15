package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openSUSE/bsgo/pkg/manifest"
	"github.com/openSUSE/bsgo/pkg/obsimport"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const convertOBSLong = `Convert an OBS project (its _meta XML and its project configuration, "prjconf")
into a bsgo project.yaml.

Conditionals are never flattened (ADR 0010). They map onto structured scopes:

  %if 0%{?_is_in_project}        protected_config
  %if 0%{?_is_this_project}      private_config
  %ifarch <arch> [<arch>...]     arch_config.<arch>
  %if "%_repository" == "<r>"    targets.<r>.config
  top-level %define              defines (order preserved)
  Macros: ... :Macros            macros (verbatim)

Everything else, and every statement that would change meaning if it moved
into its scope (the manifest renders scopes in a fixed order), stays verbatim
in raw_config. The report lists each such fragment with its reason.

Unless --no-verify is given, the result must pass the round-trip gate before
it is written: for every repository, every architecture and the Consumer
Projects (the project, a subproject, a foreign project), the layered manifest
must parse to the same configuration as the original prjconf. The command
exits non-zero and writes nothing when the gate fails.

Output: the manifest YAML goes to --out, or to stdout when --out is omitted.
With --json, stdout carries one JSON document (the report, and the manifest
when --out is omitted) and nothing else. Diagnostics go to stderr.`

const convertOBSExample = `  # Convert and write project.yaml, print a human summary to stderr
  bsgo project convert-obs --meta SUSE_SLFO_1.2.meta.xml --prjconf SUSE_SLFO_1.2.prjconf --out project.yaml

  # Machine-readable report for an agent, without writing anything
  bsgo project convert-obs --meta _meta --prjconf _config --dry-run --json

  # Replace an existing project.yaml
  bsgo project convert-obs --meta _meta --prjconf _config --out project.yaml --force`

type convertOBSFlags struct {
	meta, prjconf, out string
	verifyArches       []string
	dryRun, force      bool
	noVerify           bool
}

type convertOBSOutput struct {
	Out      string                    `json:"out,omitempty"`
	DryRun   bool                      `json:"dry_run"`
	Verified bool                      `json:"verified"`
	Report   obsimport.Report          `json:"report"`
	Manifest *manifest.ProjectManifest `json:"manifest,omitempty"`
}

func newProjectConvertOBSCmd(opts *OutputOptions) *cobra.Command {
	var f convertOBSFlags
	cmd := &cobra.Command{
		Use:     "convert-obs --meta <_meta> --prjconf <_config> [--out project.yaml]",
		Short:   "Convert an OBS project _meta and prjconf into project.yaml",
		Long:    convertOBSLong,
		Example: convertOBSExample,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConvertOBS(cmd, &f, opts)
		},
	}
	cmd.Flags().StringVar(&f.meta, "meta", "", "path to the OBS project _meta XML (required)")
	cmd.Flags().StringVar(&f.prjconf, "prjconf", "", "path to the OBS project configuration text (required)")
	cmd.Flags().StringVar(&f.out, "out", "", "write the manifest to this file (default: stdout)")
	cmd.Flags().StringSliceVar(&f.verifyArches, "verify-arch", nil,
		"extra architectures for the round-trip gate, besides those of _meta (for example i586)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "convert and verify, but do not write --out")
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite --out if it exists")
	cmd.Flags().BoolVar(&f.noVerify, "no-verify", false, "skip the round-trip gate (not recommended)")
	_ = cmd.MarkFlagRequired("meta")
	_ = cmd.MarkFlagRequired("prjconf")
	return cmd
}

func runConvertOBS(cmd *cobra.Command, f *convertOBSFlags, opts *OutputOptions) error {
	meta, conf, err := readConvertInputs(f)
	if err != nil {
		return err
	}
	res, err := obsimport.Convert(meta, conf)
	if err != nil {
		return err
	}
	verified := false
	if !f.noVerify {
		if err := verifyConversion(res.Manifest, conf, meta, f.verifyArches); err != nil {
			return err
		}
		verified = true
	}
	data, err := yaml.Marshal(res.Manifest)
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := writeConvertOutput(f, data); err != nil {
		return err
	}
	return reportConvert(cmd, f, opts, res, data, verified)
}

func readConvertInputs(f *convertOBSFlags) (*obsimport.Meta, string, error) {
	metaXML, err := os.ReadFile(filepath.Clean(f.meta))
	if err != nil {
		return nil, "", fmt.Errorf("read --meta: %w", err)
	}
	conf, err := os.ReadFile(filepath.Clean(f.prjconf))
	if err != nil {
		return nil, "", fmt.Errorf("read --prjconf: %w", err)
	}
	meta, err := obsimport.ParseMeta(metaXML)
	if err != nil {
		return nil, "", err
	}
	return meta, string(conf), nil
}

// verifyConversion runs the round-trip gate on the YAML form of the manifest,
// which is what gets committed.
func verifyConversion(prj *manifest.ProjectManifest, conf string, meta *obsimport.Meta, extra []string) error {
	data, err := yaml.Marshal(prj)
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	loaded, err := manifest.ParseProject(data)
	if err != nil {
		return fmt.Errorf("converted manifest does not load: %w", err)
	}
	seen := map[string]bool{}
	for _, a := range extra {
		seen[a] = true
	}
	for _, r := range meta.Repositories {
		for _, a := range r.Arches {
			seen[a] = true
		}
	}
	arches := make([]string, 0, len(seen))
	for a := range seen {
		arches = append(arches, a)
	}
	sort.Strings(arches)
	mismatches, err := obsimport.Verify(loaded, conf, arches)
	if err != nil {
		return err
	}
	if len(mismatches) == 0 {
		return nil
	}
	var b strings.Builder
	for _, m := range mismatches {
		fmt.Fprintf(&b, "  consumer %s repository %s arch %s (%s): %s\n", m.Consumer, m.Repo, m.Arch, m.Output, strings.Join(m.Fields, ","))
	}
	return fmt.Errorf("round-trip gate failed, nothing written:\n%s", b.String())
}

func writeConvertOutput(f *convertOBSFlags, data []byte) error {
	if f.out == "" || f.dryRun {
		return nil
	}
	out := filepath.Clean(f.out)
	if _, err := os.Stat(out); err == nil && !f.force {
		return fmt.Errorf("%s exists; use --force to overwrite", out)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(out, data, 0o600); err != nil {
		return fmt.Errorf("write --out: %w", err)
	}
	return nil
}

func reportConvert(cmd *cobra.Command, f *convertOBSFlags, opts *OutputOptions, res *obsimport.Result, data []byte, verified bool) error {
	if opts.JSON {
		doc := convertOBSOutput{Out: f.out, DryRun: f.dryRun, Verified: verified, Report: res.Report}
		if f.out == "" {
			doc.Manifest = res.Manifest
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	if f.out == "" {
		if _, err := cmd.OutOrStdout().Write(data); err != nil {
			return err
		}
	}
	r := res.Report
	fmt.Fprintf(cmd.ErrOrStderr(), "converted %s: %d structured statements, %d raw_config fragments (%d lines, %d moved only for ordering), verified=%t\n",
		r.Project, r.StructuredStatements, r.RawFragments, r.RawLines, r.OrderMoves, verified)
	return nil
}
