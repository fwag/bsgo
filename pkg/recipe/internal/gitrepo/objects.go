package gitrepo

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"
)

// fetchChunk is the number of object ids sent to one fetch.
const fetchChunk = 256

// TreeEntry is one top-level blob of a tree. Size is -1 when the blob is not
// present locally.
type TreeEntry struct {
	Name string
	OID  string
	Size int64
}

// Tree lists the top-level blobs of rev (a commit id or CommitRef).
func (g *Repo) Tree(ctx context.Context, rev string) ([]TreeEntry, error) {
	out, err := g.git(ctx, "", "ls-tree", "-z", "--long", rev+"^{tree}")
	if err != nil {
		return nil, err
	}
	var ents []TreeEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if e, ok := parseTreeEntry(string(rec)); ok {
			ents = append(ents, e)
		}
	}
	return ents, nil
}

// parseTreeEntry parses a "ls-tree --long" record for a blob:
// "<mode> blob <oid> <size>\t<name>".
func parseTreeEntry(rec string) (TreeEntry, bool) {
	meta, name, found := strings.Cut(rec, "\t")
	if !found {
		return TreeEntry{}, false
	}
	f := strings.Fields(meta)
	if len(f) != 4 || f[1] != "blob" || path.Base(name) != name {
		return TreeEntry{}, false
	}
	// "BAD" (older git: "-") is the size of a blob absent from a partial
	// clone: ls-tree does not fetch it (lazy fetches are forbidden), so the
	// size is unknown.
	size := int64(-1)
	if f[3] != "-" && f[3] != "BAD" {
		var err error
		if size, err = strconv.ParseInt(f[3], 10, 64); err != nil {
			return TreeEntry{}, false
		}
	}
	return TreeEntry{Name: name, OID: f[2], Size: size}, true
}

// ReadObjects reads the objects named by specs ("<rev>:<path>" or an object
// id) with one cat-file process and returns their contents in order. Objects
// missing from a partial clone are fetched from the remote on demand; a
// missing object that cannot be fetched, or one larger than MaxBlobSize, is
// an error.
func (g *Repo) ReadObjects(ctx context.Context, specs []string) ([][]byte, error) {
	return g.catFile(ctx, specs, false)
}

// ReadBlobs reads blobs by object id (validated hex) and returns them keyed
// by id. A blob missing locally is an error: no lazy fetch is attempted, so
// fetch with FetchBlobs first.
func (g *Repo) ReadBlobs(ctx context.Context, oids []string) (map[string][]byte, error) {
	if err := checkIDs(oids); err != nil {
		return nil, err
	}
	data, err := g.catFile(ctx, oids, true)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(oids))
	for i, id := range oids {
		out[id] = data[i]
	}
	return out, nil
}

func checkIDs(oids []string) error {
	for _, id := range oids {
		if !ValidID(id) {
			return fmt.Errorf("invalid object id %q", id)
		}
	}
	return nil
}

func (g *Repo) catFile(ctx context.Context, specs []string, noLazy bool) ([][]byte, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = g.dir
	cmd.Env = g.env(noLazy)
	cmd.Stdin = strings.NewReader(strings.Join(specs, "\n") + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := readRecords(bufio.NewReader(stdout), specs)
	if readErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, fmt.Errorf("git cat-file: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return data, nil
}

// readRecords parses one "<name> blob <size>\n<content>\n" record per spec.
func readRecords(r *bufio.Reader, specs []string) ([][]byte, error) {
	out := make([][]byte, 0, len(specs))
	for _, spec := range specs {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("cat-file output for %s: %w", spec, err)
		}
		size, err := parseHeader(strings.TrimSuffix(hdr, "\n"), spec)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+1)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fmt.Errorf("cat-file content of %s: %w", spec, err)
		}
		out = append(out, buf[:size])
	}
	return out, nil
}

// parseHeader returns the size announced by a blob record header.
func parseHeader(hdr, spec string) (int, error) {
	if strings.HasSuffix(hdr, " missing") {
		return 0, fmt.Errorf("object %s is missing", spec)
	}
	i := strings.LastIndexByte(hdr, ' ')
	if i < 0 {
		return 0, fmt.Errorf("unexpected cat-file output for %s: %q", spec, hdr)
	}
	size, err := strconv.Atoi(hdr[i+1:])
	if err != nil || size < 0 || !strings.HasSuffix(hdr[:i], " blob") {
		return 0, fmt.Errorf("unexpected cat-file output for %s: %q", spec, hdr)
	}
	if size > MaxBlobSize {
		return 0, fmt.Errorf("blob %s is too large (%d bytes)", spec, size)
	}
	return size, nil
}

// MissingBlobs returns the ids among oids that are not present locally,
// without contacting the remote.
func (g *Repo) MissingBlobs(ctx context.Context, oids []string) ([]string, error) {
	if err := checkIDs(oids); err != nil {
		return nil, err
	}
	if len(oids) == 0 {
		return nil, nil
	}
	out, err := g.git(ctx, strings.Join(oids, "\n")+"\n", "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if id, ok := strings.CutSuffix(line, " missing"); ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// FetchBlobs downloads the blobs among oids that are not present locally,
// one request per chunk of 256 ids, promisor style: a plain fetch of a blob id
// fails with "did not send all necessary objects", a filtered fetch with the
// ids on stdin does not. It returns the number of blobs requested.
func (g *Repo) FetchBlobs(ctx context.Context, oids []string) (int, error) {
	missing, err := g.MissingBlobs(ctx, oids)
	if err != nil {
		return 0, err
	}
	total := len(missing)
	for len(missing) > 0 {
		n := min(fetchChunk, len(missing))
		_, err := g.git(ctx, strings.Join(missing[:n], "\n")+"\n",
			"-c", "fetch.negotiationAlgorithm=noop", "fetch", "--quiet", "src", "--no-tags",
			"--no-write-fetch-head", "--recurse-submodules=no", "--filter=blob:none", "--stdin")
		if err != nil {
			return 0, fmt.Errorf("fetch %d blobs: %w", n, err)
		}
		missing = missing[n:]
	}
	return total, nil
}
