package v1_test

import (
	"encoding/json"
	"testing"
	"time"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
	"github.com/openSUSE/bsgo/pkg/publisher"
)

func TestSLSA12ProvenanceGenerationAndValidation(t *testing.T) {
	job := &v1.BuildJob{
		JobID:        "job-slsa-test-01",
		Project:      "openSUSE:Factory",
		Package:      "ripgrep",
		Target:       "standard",
		Architecture: "x86_64",
		Priority:     1050,
		Reason:       "SOURCE_CHANGE",
		Source: v1.SourceSpec{
			GitURL:     "https://github.com/BurntSushi/ripgrep.git",
			CommitSHA:  "e5c6a7b8c9d01234567890abcdef1234567890ab",
			RecipePath: "ripgrep.spec",
		},
		ChrootRPMs: []v1.ChrootRPM{
			{
				Filename: "glibc-2.38-1.x86_64.rpm",
				S3Key:    "repos/openSUSE:Factory/standard/x86_64/glibc-2.38-1.x86_64.rpm",
				SHA256:   "aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff66667777888899990000",
			},
		},
		Resources: v1.ResourceRequirements{
			MemoryMB:    4096,
			VCPUs:       2,
			SandboxType: "kvm",
		},
	}

	rpmDigest := "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"
	artifacts := []v1.ChrootRPM{
		{
			Filename:  "ripgrep-14.1.0-1.x86_64.rpm",
			S3Key:     "artifacts/openSUSE:Factory/ripgrep/ripgrep-14.1.0-1.x86_64.rpm",
			SHA256:    rpmDigest,
			SizeBytes: 5242880,
		},
	}

	startedOn := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	finishedOn := startedOn.Add(45 * time.Second)

	stmt := v1.GenerateSLSAProvenance(job, artifacts, "worker-node-42", startedOn, finishedOn)

	// 1. Verify in-toto Envelope
	if stmt.Type != v1.InTotoStatementType {
		t.Errorf("expected statement type %s, got %s", v1.InTotoStatementType, stmt.Type)
	}
	if stmt.PredicateType != v1.SLSAProvenanceType {
		t.Errorf("expected predicate type %s, got %s", v1.SLSAProvenanceType, stmt.PredicateType)
	}

	// 2. Verify Subject
	if len(stmt.Subject) != 1 {
		t.Fatalf("expected 1 subject artifact, got %d", len(stmt.Subject))
	}
	sub := stmt.Subject[0]
	if sub.Name != "ripgrep-14.1.0-1.x86_64.rpm" {
		t.Errorf("expected subject name ripgrep-14.1.0-1.x86_64.rpm, got %s", sub.Name)
	}
	if sub.Digest["sha256"] != rpmDigest {
		t.Errorf("expected subject sha256 %s, got %s", rpmDigest, sub.Digest["sha256"])
	}

	// 3. Verify Build Definition
	bdef := stmt.Predicate.BuildDefinition
	if bdef.BuildType != v1.BSGOBuildType {
		t.Errorf("expected build type %s, got %s", v1.BSGOBuildType, bdef.BuildType)
	}
	if len(bdef.ResolvedDependencies) != 1 {
		t.Fatalf("expected 1 resolved dependency, got %d", len(bdef.ResolvedDependencies))
	}
	if bdef.ResolvedDependencies[0].Digest["sha256"] != "aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff66667777888899990000" {
		t.Errorf("resolved dependency digest mismatch")
	}

	// 4. Verify Builder & Run Details
	rdet := stmt.Predicate.RunDetails
	if rdet.Builder.ID != "https://github.com/openSUSE/bsgo/worker/worker-node-42" {
		t.Errorf("expected builder ID, got %s", rdet.Builder.ID)
	}
	if rdet.Metadata.InvocationID != "job-slsa-test-01" {
		t.Errorf("expected invocation ID job-slsa-test-01, got %s", rdet.Metadata.InvocationID)
	}

	// 5. Verify JSON serialization round-trip and Publisher validation
	jsonData, err := json.Marshal(stmt)
	if err != nil {
		t.Fatalf("failed to marshal in-toto statement: %v", err)
	}

	expectedRPMs := map[string]string{
		"ripgrep-14.1.0-1.x86_64.rpm": rpmDigest,
	}
	validatedStmt, err := publisher.ValidateProvenance(jsonData, expectedRPMs)
	if err != nil {
		t.Fatalf("publisher validation failed: %v", err)
	}
	if validatedStmt.Subject[0].Name != sub.Name {
		t.Errorf("validated subject mismatch")
	}

	// 6. Verify tampering rejection
	tamperedRPMs := map[string]string{
		"ripgrep-14.1.0-1.x86_64.rpm": "tampered-hash-0000000000000000000000000000000000000000000000000000",
	}
	_, err = publisher.ValidateProvenance(jsonData, tamperedRPMs)
	if err == nil {
		t.Fatalf("expected publisher to reject tampered hash, but validation succeeded")
	}
}
