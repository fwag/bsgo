package manifest

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

var quantityUnits = []struct {
	suffix string
	mb     int64 // size of the unit in MiB
}{
	{"Ti", 1 << 20}, {"Gi", 1 << 10}, {"Mi", 1}, {"T", 1 << 20}, {"G", 1 << 10}, {"M", 1},
}

// ParseQuantity parses "<n>[Ti|Gi|Mi|T|G|M]" and returns the amount in units
// of per MiB: with per=1 the result is MiB, with per=1024 it is GiB. A plain
// number is in the result unit.
func ParseQuantity(s string, per int64) (int64, error) {
	s = strings.TrimSpace(s)
	for _, u := range quantityUnits {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid quantity %q", s)
			}
			return max(n*u.mb/per, 1), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid quantity %q", s)
	}
	return n, nil
}

// ParseTimeoutSeconds parses a duration ("8h", "90m") or a plain number of
// seconds.
func ParseTimeoutSeconds(s string) (int32, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseInt(s, 10, 32); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("negative timeout %q", s)
		}
		return int32(n), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 || d > 1<<30*time.Second {
		return 0, fmt.Errorf("invalid timeout %q", s)
	}
	return int32(d / time.Second), nil
}

// Validate checks that every set field parses. Empty fields are valid: they
// mean "use the default".
func (l ResourceLimits) Validate() error {
	if l.VCPUs < 0 {
		return fmt.Errorf("vcpus: negative value %d", l.VCPUs)
	}
	if l.Memory != "" {
		if _, err := ParseQuantity(l.Memory, 1); err != nil {
			return fmt.Errorf("memory: %w", err)
		}
	}
	if l.Disk != "" {
		if _, err := ParseQuantity(l.Disk, 1<<10); err != nil {
			return fmt.Errorf("disk: %w", err)
		}
	}
	if l.Timeout != "" {
		if _, err := ParseTimeoutSeconds(l.Timeout); err != nil {
			return fmt.Errorf("timeout: %w", err)
		}
	}
	return nil
}

// Merge returns l with every non-zero field of o replacing the one in l.
func (l ResourceLimits) Merge(o ResourceLimits) ResourceLimits {
	if o.Memory != "" {
		l.Memory = o.Memory
	}
	if o.VCPUs > 0 {
		l.VCPUs = o.VCPUs
	}
	if o.Disk != "" {
		l.Disk = o.Disk
	}
	if o.Timeout != "" {
		l.Timeout = o.Timeout
	}
	return l
}
