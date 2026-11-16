// Package signing is the RPM signing stage between a finished build and the
// integration of its binaries into the build repository (OBS: job state
// "signing", bs_signer:signjob). Workers never see keys: the stage downloads
// the unsigned RPMs, signs them with rpmsign through bsgo-sign-helper (which
// asks bsgo-signd for the signature), verifies them against the project public
// key, re-extracts the binary index and uploads the signed files in place.
package signing

import (
	"encoding/json"
	"fmt"

	"github.com/openSUSE/bsgo/pkg/scheduler/indexer"
)

// Job is the payload of both signing messages. A request (job-finished) lists
// the unsigned artifacts as the worker reported them. The answer (job-signed)
// lists the artifacts after signing, with their new sha256, or carries Error.
type Job struct {
	JobID        string             `json:"job_id"`
	Project      string             `json:"project"`
	Package      string             `json:"package"`
	Target       string             `json:"target"`
	Architecture string             `json:"architecture"`
	Meta         string             `json:"meta,omitempty"`
	SourceCommit string             `json:"source_commit,omitempty"`
	Artifacts    []indexer.Artifact `json:"artifacts"`
	// Error is set on an answer when signing failed for good.
	Error string `json:"error,omitempty"`
}

// Decode parses a signing message.
func Decode(data []byte) (Job, error) {
	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return Job{}, fmt.Errorf("malformed signing message: %w", err)
	}
	if j.JobID == "" || j.Project == "" || j.Package == "" || j.Target == "" || j.Architecture == "" {
		return Job{}, fmt.Errorf("signing message of job %q lacks identity", j.JobID)
	}
	return j, nil
}

// Encode serializes a signing message.
func Encode(j Job) ([]byte, error) { return json.Marshal(j) }

// HasRPMs reports whether the job produced anything the stage signs.
func HasRPMs(arts []indexer.Artifact) bool {
	for _, a := range arts {
		if isRPM(a.Filename) {
			return true
		}
	}
	return false
}
