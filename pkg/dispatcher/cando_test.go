package dispatcher_test

import (
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/dispatcher"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestDispatchMatchesJobArchAgainstWorkerArchs(t *testing.T) {
	tests := []struct {
		name    string
		worker  v1.WorkerHeartbeat
		jobArch string
		want    bool
	}{
		{"native arch, no list", v1.WorkerHeartbeat{Architecture: "x86_64"}, "x86_64", true},
		{"legacy worker cannot build i586", v1.WorkerHeartbeat{Architecture: "x86_64"}, "i586", false},
		{"cando list builds i586", v1.WorkerHeartbeat{Architecture: "x86_64", Archs: []string{"x86_64", "i586"}}, "i586", true},
		{"cando list excludes i686", v1.WorkerHeartbeat{Architecture: "x86_64", Archs: []string{"x86_64", "i586"}}, "i686", false},
		{"explicit list wins over host arch", v1.WorkerHeartbeat{Architecture: "x86_64", Archs: []string{"i586"}}, "x86_64", false},
		{"other host", v1.WorkerHeartbeat{Architecture: "aarch64", Archs: []string{"aarch64"}}, "x86_64", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disp := dispatcher.NewDispatcher(dispatcher.NewMockPublisher(nil), 30*time.Second)
			tt.worker.WorkerID, tt.worker.Status = "w1", "IDLE"
			tt.worker.Hardware = v1.WorkerHardware{TotalVCPUs: 4, TotalMemoryMB: 8192}
			tt.worker.TimestampUnix = time.Now().Unix()
			disp.Registry().UpdateHeartbeat(tt.worker)
			job := &v1.BuildJob{JobID: "j", Project: "p", Package: "glibc", Target: "standard", Architecture: tt.jobArch, Priority: 1}
			if w, _ := disp.Registry().FindBestIdleWorker(job); (w != nil) != tt.want {
				t.Errorf("FindBestIdleWorker found=%v, want %v", w != nil, tt.want)
			}
		})
	}
}
