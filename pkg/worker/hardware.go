package worker

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	v1 "github.com/openSUSE/bsgo/pkg/proto/v1"
)

const (
	defaultReserveMemoryMB = 1024
	minMemoryMB            = 256
	bytesPerMB             = 1024 * 1024
	bytesPerGB             = 1024 * 1024 * 1024
)

// HardwareOverrides are the explicit operator settings (config file or flags).
// A zero or nil field means "not set": the detected value is used.
type HardwareOverrides struct {
	VCPUs           int32
	MemoryMB        int64
	DiskGB          int64
	ReserveMemoryMB *int64
	ReserveVCPUs    *int32
}

// DetectHardware reports the resources usable by this process: CPUs allowed by
// the scheduler affinity and the cgroup v2 cpu quota, memory bounded by
// MemTotal and the cgroup v2 memory limit, and the free space of workDir.
// procRoot and cgroupRoot are normally "/proc" and "/sys/fs/cgroup".
func DetectHardware(procRoot, cgroupRoot, workDir string) (v1.WorkerHardware, error) {
	mem, err := DetectMemoryMB(procRoot, cgroupRoot)
	if err != nil {
		return v1.WorkerHardware{}, err
	}
	disk, err := DetectDiskGB(workDir)
	if err != nil {
		return v1.WorkerHardware{}, err
	}
	return v1.WorkerHardware{
		TotalVCPUs:      DetectVCPUs(procRoot, cgroupRoot, runtime.NumCPU()),
		TotalMemoryMB:   mem,
		AvailableDiskGB: disk,
	}, nil
}

// DetectVCPUs returns min(numCPU, cgroup v2 cpu.max quota), at least 1.
// numCPU is the affinity-aware CPU count (runtime.NumCPU).
func DetectVCPUs(procRoot, cgroupRoot string, numCPU int) int32 {
	n := int64(numCPU)
	for _, dir := range cgroupChain(procRoot, cgroupRoot) {
		fields := strings.Fields(readTrim(filepath.Join(dir, "cpu.max")))
		if len(fields) != 2 || fields[0] == "max" {
			continue
		}
		quota, err1 := strconv.ParseInt(fields[0], 10, 64)
		period, err2 := strconv.ParseInt(fields[1], 10, 64)
		if err1 != nil || err2 != nil || quota <= 0 || period <= 0 {
			continue
		}
		n = min(n, quota/period)
	}
	return int32(min(max(n, 1), math.MaxInt32))
}

// DetectMemoryMB returns min(MemTotal, cgroup v2 memory.max) in MiB.
func DetectMemoryMB(procRoot, cgroupRoot string) (int64, error) {
	total, err := memTotalMB(filepath.Join(procRoot, "meminfo"))
	if err != nil {
		return 0, err
	}
	for _, dir := range cgroupChain(procRoot, cgroupRoot) {
		raw := readTrim(filepath.Join(dir, "memory.max"))
		limit, perr := strconv.ParseInt(raw, 10, 64)
		if raw == "" || perr != nil || limit <= 0 {
			continue
		}
		total = min(total, limit/bytesPerMB)
	}
	return total, nil
}

// DetectDiskGB returns the free space in GiB of the filesystem holding path,
// or of its closest existing parent when path does not exist yet.
func DetectDiskGB(path string) (int64, error) {
	p := filepath.Clean(path)
	for {
		var st syscall.Statfs_t
		err := syscall.Statfs(p, &st)
		if err == nil {
			free := new(big.Int).Mul(new(big.Int).SetUint64(st.Bavail), big.NewInt(st.Bsize))
			return free.Div(free, big.NewInt(bytesPerGB)).Int64(), nil
		}
		parent := filepath.Dir(p)
		if !errors.Is(err, syscall.ENOENT) || parent == p {
			return 0, fmt.Errorf("statfs %s: %w", p, err)
		}
		p = parent
	}
}

func memTotalMB(meminfo string) (int64, error) {
	f, err := os.Open(filepath.Clean(meminfo))
	if err != nil {
		return 0, fmt.Errorf("reading meminfo: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parsing MemTotal: %w", err)
		}
		return kb / 1024, nil
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("reading meminfo: %w", err)
	}
	return 0, errors.New("MemTotal not found in meminfo")
}

// cgroupChain lists the cgroup v2 directories bounding this process: its own
// cgroup (from <procRoot>/self/cgroup) and every ancestor up to cgroupRoot.
// Without cgroup v2 information it is just cgroupRoot.
func cgroupChain(procRoot, cgroupRoot string) []string {
	own := ""
	for _, line := range strings.Split(readTrim(filepath.Join(procRoot, "self", "cgroup")), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			own = filepath.Clean("/" + rest)
		}
	}
	chain := []string{cgroupRoot}
	for p := own; p != "/" && p != "." && p != ""; p = filepath.Dir(p) {
		chain = append(chain, filepath.Join(cgroupRoot, p))
	}
	return chain
}

func readTrim(path string) string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// EffectiveHardware combines the detected resources with the operator
// overrides. An explicit value wins over detection; detected values have the
// host reserve subtracted (default 1 GiB memory, 0 vCPUs) but never drop below
// 1 vCPU / 256 MiB.
func EffectiveHardware(det v1.WorkerHardware, ov HardwareOverrides) v1.WorkerHardware {
	out := det
	reserveMem := int64(defaultReserveMemoryMB)
	if ov.ReserveMemoryMB != nil {
		reserveMem = *ov.ReserveMemoryMB
	}
	var reserveCPU int32
	if ov.ReserveVCPUs != nil {
		reserveCPU = *ov.ReserveVCPUs
	}
	out.TotalVCPUs = max(det.TotalVCPUs-reserveCPU, 1)
	out.TotalMemoryMB = max(det.TotalMemoryMB-reserveMem, minMemoryMB)
	if ov.VCPUs > 0 {
		out.TotalVCPUs = ov.VCPUs
	}
	if ov.MemoryMB > 0 {
		out.TotalMemoryMB = ov.MemoryMB
	}
	if ov.DiskGB > 0 {
		out.AvailableDiskGB = ov.DiskGB
	}
	out.AvailableVCPUs = out.TotalVCPUs
	out.AvailableMemoryMB = out.TotalMemoryMB
	return out
}

// DescribeHardware renders hardware for the one-time startup log line.
func DescribeHardware(hw v1.WorkerHardware) string {
	return fmt.Sprintf("vcpus=%d memory_mb=%d disk_gb=%d", hw.TotalVCPUs, hw.TotalMemoryMB, hw.AvailableDiskGB)
}
