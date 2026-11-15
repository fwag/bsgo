package bininfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// RPM sense flag bits (rpmds.h).
const (
	senseLess     = 1 << 1
	senseGreater  = 1 << 2
	senseEqual    = 1 << 3
	sensePrereq   = 1 << 6
	senseScriptPr = 1 << 9
	senseScriptPo = 1 << 10
)

// queryFormat prints one header line followed by one line per dependency.
// Each bracketed group iterates one tag family; fields are tab separated and
// may be empty, so parsing must not collapse whitespace.
const queryFormat = `%{NAME}\t%{EPOCH}\t%{VERSION}\t%{RELEASE}\t%{ARCH}\t%{SOURCERPM}\n` +
	`[P\t%{PROVIDENAME}\t%{PROVIDEFLAGS}\t%{PROVIDEVERSION}\n]` +
	`[R\t%{REQUIRENAME}\t%{REQUIREFLAGS}\t%{REQUIREVERSION}\n]` +
	`[C\t%{CONFLICTNAME}\t%{CONFLICTFLAGS}\t%{CONFLICTVERSION}\n]` +
	`[O\t%{OBSOLETENAME}\t%{OBSOLETEFLAGS}\t%{OBSOLETEVERSION}\n]` +
	`[M\t%{RECOMMENDNAME}\t%{RECOMMENDFLAGS}\t%{RECOMMENDVERSION}\n]` +
	`[G\t%{SUGGESTNAME}\t%{SUGGESTFLAGS}\t%{SUGGESTVERSION}\n]` +
	`[S\t%{SUPPLEMENTNAME}\t%{SUPPLEMENTFLAGS}\t%{SUPPLEMENTVERSION}\n]` +
	`[E\t%{ENHANCENAME}\t%{ENHANCEFLAGS}\t%{ENHANCEVERSION}\n]` +
	`[F\t%{FILENAMES}\n]`

// Extract reads the header of the RPM at path using "rpm -qp". The package
// does not need to be installed and its signature is not checked here (the
// signer owns that); header and payload digests are still verified by rpm.
func Extract(ctx context.Context, path string) (BinInfo, error) {
	// #nosec G204 -- fixed argv, path is passed as a single argument after "--"
	cmd := exec.CommandContext(ctx, "rpm", "-qp", "--nosignature", "--qf", queryFormat, "--", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return BinInfo{}, fmt.Errorf("rpm -qp %s: %w: %s", filepath.Base(path), err, strings.TrimSpace(stderr.String()))
	}
	info, err := parseQuery(stdout.String())
	if err != nil {
		return BinInfo{}, fmt.Errorf("parsing header of %s: %w", filepath.Base(path), err)
	}
	if err := fillFileIdentity(&info, path); err != nil {
		return BinInfo{}, err
	}
	return info, nil
}

// ExtractAll extracts every path and returns the infos in input order. The
// first failure aborts and is returned.
func ExtractAll(ctx context.Context, paths []string) ([]BinInfo, error) {
	infos := make([]BinInfo, 0, len(paths))
	for _, p := range paths {
		info, err := Extract(ctx, p)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func fillFileIdentity(info *BinInfo, path string) error {
	// #nosec G304 -- path is a build artifact inside the job directory
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return fmt.Errorf("hashing %s: %w", path, err)
	}
	info.SHA256 = hex.EncodeToString(h.Sum(nil))
	info.Size = n
	info.Filename = filepath.Base(path)
	return nil
}

func noneToEmpty(s string) string {
	if s == "(none)" {
		return ""
	}
	return s
}

func parseQuery(out string) (BinInfo, error) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	head := strings.Split(lines[0], "\t")
	if len(head) != 6 {
		return BinInfo{}, fmt.Errorf("unexpected header line %q", lines[0])
	}
	info := BinInfo{
		Name: head[0], Epoch: noneToEmpty(head[1]), Version: head[2],
		Release: head[3], Arch: head[4], SourceRPM: noneToEmpty(head[5]),
	}
	if info.Name == "" || info.Version == "" || info.Arch == "" {
		return BinInfo{}, fmt.Errorf("incomplete header line %q", lines[0])
	}
	targets := map[string]*[]Dep{
		"P": &info.Provides, "R": &info.Requires, "C": &info.Conflicts,
		"O": &info.Obsoletes, "M": &info.Recommends, "G": &info.Suggests,
		"S": &info.Supplements, "E": &info.Enhances,
	}
	for _, line := range lines[1:] {
		if path, ok := strings.CutPrefix(line, "F\t"); ok {
			if IsPrimaryFile(path) {
				info.Files = append(info.Files, path)
			}
			continue
		}
		kind, dep, err := parseDepLine(line)
		if err != nil {
			return BinInfo{}, err
		}
		dst, ok := targets[kind]
		if !ok {
			return BinInfo{}, fmt.Errorf("unknown dependency kind in %q", line)
		}
		*dst = append(*dst, dep)
	}
	return info, nil
}

func parseDepLine(line string) (string, Dep, error) {
	f := strings.Split(line, "\t")
	if len(f) != 4 {
		return "", Dep{}, fmt.Errorf("unexpected dependency line %q", line)
	}
	flags, err := strconv.ParseUint(f[2], 10, 32)
	if err != nil {
		return "", Dep{}, fmt.Errorf("bad dependency flags in %q: %w", line, err)
	}
	op, err := senseOp(flags)
	if err != nil {
		return "", Dep{}, fmt.Errorf("dependency %q: %w", line, err)
	}
	d := Dep{Name: f[1], Op: op, EVR: f[3]}
	if f[0] == "R" {
		d.Pre = flags&(sensePrereq|senseScriptPr|senseScriptPo) != 0
	}
	return f[0], d, nil
}

func senseOp(flags uint64) (string, error) {
	switch flags & (senseLess | senseGreater | senseEqual) {
	case 0:
		return "", nil
	case senseLess:
		return "<", nil
	case senseLess | senseEqual:
		return "<=", nil
	case senseEqual:
		return "=", nil
	case senseGreater | senseEqual:
		return ">=", nil
	case senseGreater:
		return ">", nil
	default:
		return "", fmt.Errorf("invalid comparison flags %d", flags)
	}
}
