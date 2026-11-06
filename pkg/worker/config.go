package worker

import (
	"context"
	"io"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

type WorkerConfig struct {
	WorkerID     string
	Architecture string
	NatsURL      string
	S3Endpoint   string
	S3Bucket     string
	WorkDir      string
	CacheDir     string
	Hardware     v1.WorkerHardware
}

type ExecutionResult struct {
	ExitCode          int
	Artifacts         []v1.ChrootRPM
	PreinstallSeconds float64
	MainBuildSeconds  float64
	TotalSeconds      float64
}

type Sandbox interface {
	Execute(ctx context.Context, job *v1.BuildJob, logWriter io.Writer) (*ExecutionResult, error)
	Cleanup(ctx context.Context) error
}
