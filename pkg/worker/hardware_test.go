package worker

import (
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetectVCPUs(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		cpus  int
		want  int32
	}{
		{"no cgroup info", nil, 4, 4},
		{"max quota", map[string]string{"cg/cpu.max": "max 100000\n"}, 4, 4},
		{"quota below cpus", map[string]string{"cg/cpu.max": "200000 100000\n"}, 8, 2},
		{"fractional quota floors", map[string]string{"cg/cpu.max": "150000 100000\n"}, 8, 1},
		{"tiny quota min one", map[string]string{"cg/cpu.max": "10000 100000\n"}, 8, 1},
		{"quota above cpus", map[string]string{"cg/cpu.max": "800000 100000\n"}, 2, 2},
		{"garbage ignored", map[string]string{"cg/cpu.max": "x y\n"}, 3, 3},
		{"own cgroup limit", map[string]string{
			"proc/self/cgroup": "0::/a/b\n",
			"cg/a/b/cpu.max":   "100000 100000\n",
			"cg/a/cpu.max":     "max 100000\n",
		}, 8, 1},
		{"ancestor limit", map[string]string{
			"proc/self/cgroup": "0::/a/b\n",
			"cg/a/cpu.max":     "300000 100000\n",
		}, 8, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, tc.files)
			got := DetectVCPUs(filepath.Join(root, "proc"), filepath.Join(root, "cg"), tc.cpus)
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDetectMemoryMB(t *testing.T) {
	const meminfo = "MemTotal:       24117248 kB\nMemFree: 1 kB\n" // 23552 MiB
	tests := []struct {
		name    string
		files   map[string]string
		want    int64
		wantErr bool
	}{
		{"meminfo only", map[string]string{"proc/meminfo": meminfo}, 23552, false},
		{"memory.max max", map[string]string{"proc/meminfo": meminfo, "cg/memory.max": "max\n"}, 23552, false},
		{"cgroup lower", map[string]string{"proc/meminfo": meminfo, "cg/memory.max": "4294967296\n"}, 4096, false},
		{"cgroup higher", map[string]string{"proc/meminfo": meminfo, "cg/memory.max": "99999999999999\n"}, 23552, false},
		{"own cgroup", map[string]string{
			"proc/meminfo": meminfo, "proc/self/cgroup": "0::/svc\n", "cg/svc/memory.max": "2147483648\n",
		}, 2048, false},
		{"no meminfo", nil, 0, true},
		{"no MemTotal", map[string]string{"proc/meminfo": "MemFree: 1 kB\n"}, 0, true},
		{"bad MemTotal", map[string]string{"proc/meminfo": "MemTotal: abc kB\n"}, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, tc.files)
			got, err := DetectMemoryMB(filepath.Join(root, "proc"), filepath.Join(root, "cg"))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDetectDiskGB(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{dir, filepath.Join(dir, "missing", "deeper")} {
		got, err := DetectDiskGB(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if got < 0 {
			t.Fatalf("%s: negative free space %d", p, got)
		}
	}
}

func TestEffectiveHardware(t *testing.T) {
	det := v1.WorkerHardware{TotalVCPUs: 4, TotalMemoryMB: 23552, AvailableDiskGB: 100}
	i64 := func(v int64) *int64 { return &v }
	i32 := func(v int32) *int32 { return &v }
	tests := []struct {
		name          string
		det           v1.WorkerHardware
		ov            HardwareOverrides
		vcpus         int32
		memMB, diskGB int64
	}{
		{"default reserve", det, HardwareOverrides{}, 4, 22528, 100},
		{"explicit reserve", det, HardwareOverrides{ReserveMemoryMB: i64(2048), ReserveVCPUs: i32(1)}, 3, 21504, 100},
		{"zero reserve", det, HardwareOverrides{ReserveMemoryMB: i64(0)}, 4, 23552, 100},
		{"explicit wins", det, HardwareOverrides{VCPUs: 2, MemoryMB: 8192, DiskGB: 10, ReserveVCPUs: i32(3)}, 2, 8192, 10},
		{"reserve floors", v1.WorkerHardware{TotalVCPUs: 1, TotalMemoryMB: 512}, HardwareOverrides{ReserveVCPUs: i32(4)}, 1, 256, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveHardware(tc.det, tc.ov)
			if got.TotalVCPUs != tc.vcpus || got.TotalMemoryMB != tc.memMB || got.AvailableDiskGB != tc.diskGB {
				t.Fatalf("got %s", DescribeHardware(got))
			}
			if got.AvailableVCPUs != got.TotalVCPUs || got.AvailableMemoryMB != got.TotalMemoryMB {
				t.Fatalf("available not reset: %+v", got)
			}
		})
	}
}
