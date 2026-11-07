package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	InTotoStatementType = "https://in-toto.io/Statement/v1"
	SLSAProvenanceType  = "https://slsa.dev/provenance/v1"
	BSGOBuildType       = "https://github.com/openSUSE/bsgo/buildtypes/obs-build/v1"
)

// InTotoStatement represents an in-toto v1.0 Attestation Statement wrapper.
type InTotoStatement struct {
	Type          string            `json:"_type"`
	Subject       []SubjectArtifact `json:"subject"`
	PredicateType string            `json:"predicateType"`
	Predicate     SLSAPredicate     `json:"predicate"`
}

// SubjectArtifact represents an artifact produced by the build and its cryptographic digests.
type SubjectArtifact struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// SLSAPredicate represents a SLSA v1.2 provenance predicate.
type SLSAPredicate struct {
	BuildDefinition SLSABuildDefinition `json:"buildDefinition"`
	RunDetails      SLSARunDetails      `json:"runDetails"`
}

type SLSABuildDefinition struct {
	BuildType            string               `json:"buildType"`
	ExternalParameters   map[string]any       `json:"externalParameters"`
	InternalParameters   map[string]any       `json:"internalParameters,omitempty"`
	ResolvedDependencies []ResolvedDependency `json:"resolvedDependencies,omitempty"`
}

type ResolvedDependency struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest"`
}

type SLSARunDetails struct {
	Builder  SLSABuilder  `json:"builder"`
	Metadata SLSAMetadata `json:"metadata"`
}

type SLSABuilder struct {
	ID string `json:"id"`
}

type SLSAMetadata struct {
	InvocationID string `json:"invocationId"`
	StartedOn    string `json:"startedOn"`
	FinishedOn   string `json:"finishedOn"`
}

// ComputeSHA256 returns the lowercase hex string representation of the sha256 checksum of data.
func ComputeSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// GenerateSLSAProvenance creates an in-toto v1.0 statement containing a SLSA 1.2 build provenance predicate.
func GenerateSLSAProvenance(job *BuildJob, artifacts []ChrootRPM, builderID string, startedOn, finishedOn time.Time) *InTotoStatement {
	var subjects []SubjectArtifact
	for _, art := range artifacts {
		digest := art.SHA256
		if digest == "" {
			digest = "0000000000000000000000000000000000000000000000000000000000000000"
		}
		subjects = append(subjects, SubjectArtifact{
			Name: art.Filename,
			Digest: map[string]string{
				"sha256": digest,
			},
		})
	}

	var resolvedDeps []ResolvedDependency
	for _, dep := range job.ChrootRPMs {
		digest := dep.SHA256
		if digest == "" {
			digest = "mock-sha256"
		}
		resolvedDeps = append(resolvedDeps, ResolvedDependency{
			URI: fmt.Sprintf("s3://%s", dep.S3Key),
			Digest: map[string]string{
				"sha256": digest,
			},
		})
	}

	builderURI := fmt.Sprintf("https://github.com/openSUSE/bsgo/worker/%s", builderID)
	if builderID == "" {
		builderURI = "https://github.com/openSUSE/bsgo/worker/unknown"
	}

	return &InTotoStatement{
		Type:          InTotoStatementType,
		Subject:       subjects,
		PredicateType: SLSAProvenanceType,
		Predicate: SLSAPredicate{
			BuildDefinition: SLSABuildDefinition{
				BuildType: BSGOBuildType,
				ExternalParameters: map[string]any{
					"source": map[string]string{
						"git_url":    job.Source.GitURL,
						"commit_sha": job.Source.CommitSHA,
						"recipe":     job.Source.RecipePath,
					},
					"target":       job.Target,
					"architecture": job.Architecture,
					"project":      job.Project,
					"package":      job.Package,
				},
				InternalParameters: map[string]any{
					"sandbox_type": job.Resources.SandboxType,
					"vcpus":        job.Resources.VCPUs,
					"memory_mb":    job.Resources.MemoryMB,
				},
				ResolvedDependencies: resolvedDeps,
			},
			RunDetails: SLSARunDetails{
				Builder: SLSABuilder{
					ID: builderURI,
				},
				Metadata: SLSAMetadata{
					InvocationID: job.JobID,
					StartedOn:    startedOn.UTC().Format(time.RFC3339),
					FinishedOn:   finishedOn.UTC().Format(time.RFC3339),
				},
			},
		},
	}
}
