package dispatcher_test

import (
	"testing"
	"time"

	"github.com/openSUSE/bsgo/pkg/dispatcher"
	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func TestDispatchMatchesSandboxConstraints(t *testing.T) {
	kvm := []v1.SandboxConstraint{{Value: "kvm"}}
	tests := []struct {
		name        string
		sandbox     string
		constrained bool
		job         *v1.JobConstraints
		want        bool
	}{
		{"unconstrained job on nspawn", "nspawn", false, nil, true},
		{"empty constraints on nspawn", "nspawn", false, &v1.JobConstraints{MemoryMB: 1}, true},
		{"kvm job on nspawn", "nspawn", false, &v1.JobConstraints{Sandbox: kvm}, false},
		{"kvm job on kvm", "kvm", false, &v1.JobConstraints{Sandbox: kvm}, true},
		{"kvm job on worker without sandbox type", "", false, &v1.JobConstraints{Sandbox: kvm}, false},
		{"secure job on kvm", "kvm", false, &v1.JobConstraints{Sandbox: []v1.SandboxConstraint{{Value: "secure"}}}, true},
		{"secure job on nspawn", "nspawn", false, &v1.JobConstraints{Sandbox: []v1.SandboxConstraint{{Value: "secure"}}}, false},
		{"secure job on chroot", "chroot", false, &v1.JobConstraints{Sandbox: []v1.SandboxConstraint{{Value: "secure"}}}, false},
		{"exclude chroot on kvm", "kvm", false, &v1.JobConstraints{Sandbox: []v1.SandboxConstraint{{Value: "chroot", Exclude: true}}}, true},
		{"exclude chroot on chroot", "chroot", false, &v1.JobConstraints{Sandbox: []v1.SandboxConstraint{{Value: "chroot", Exclude: true}}}, false},
		{"exclude on worker without sandbox type", "", false, &v1.JobConstraints{Sandbox: []v1.SandboxConstraint{{Value: "chroot", Exclude: true}}}, true},
		{"constrained-only worker refuses unconstrained job", "kvm", true, nil, false},
		{"constrained-only worker refuses non-sandbox constraints", "kvm", true, &v1.JobConstraints{MemoryMB: 1}, false},
		{"constrained-only worker takes kvm job", "kvm", true, &v1.JobConstraints{Sandbox: kvm}, true},
		{"constrained-only worker refuses other sandbox job", "kvm", true,
			&v1.JobConstraints{Sandbox: []v1.SandboxConstraint{{Value: "xen"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disp := dispatcher.NewDispatcher(dispatcher.NewMockPublisher(nil), 30*time.Second)
			disp.Registry().UpdateHeartbeat(v1.WorkerHeartbeat{
				WorkerID: "w1", Status: "IDLE", Architecture: "x86_64", ConstrainedOnly: tt.constrained,
				Hardware:      v1.WorkerHardware{TotalVCPUs: 4, TotalMemoryMB: 8192, SandboxType: tt.sandbox},
				TimestampUnix: time.Now().Unix(),
			})
			job := &v1.BuildJob{JobID: "j", Project: "p", Package: "util-linux", Target: "standard",
				Architecture: "x86_64", Priority: 1, Constraints: tt.job}
			if w, _ := disp.Registry().FindBestIdleWorker(job); (w != nil) != tt.want {
				t.Errorf("FindBestIdleWorker found=%v, want %v", w != nil, tt.want)
			}
		})
	}
}

func TestSandboxAllowsNilConstraints(t *testing.T) {
	var c *v1.JobConstraints
	if !c.SandboxAllows("anything") || c.SandboxConstrained() {
		t.Fatal("nil constraints must allow every sandbox")
	}
}
