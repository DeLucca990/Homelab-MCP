package system

import (
	"context"
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

// CoreUsage is the breakdown of a single core, in the spirit of htop's bars.
type CoreUsage struct {
	Core string `json:"core"` // "cpu0", "cpu1", ...

	TotalPercent  float64 `json:"total_percent"` // real work: user+system+nice+irq+steal
	UserPercent   float64 `json:"user_percent"`
	SystemPercent float64 `json:"system_percent"` // kernel / syscalls
	NicePercent   float64 `json:"nice_percent"`
	IRQPercent    float64 `json:"irq_percent"`   // hard + soft
	StealPercent  float64 `json:"steal_percent"` // taken by the hypervisor

	// Stalled on disk or network. Technically idle, so it stays out of the
	// total — but it is the signal when the server feels slow at low CPU.
	IOWaitPercent float64 `json:"iowait_percent"`
	IdlePercent   float64 `json:"idle_percent"`
}

const (
	defaultCPUInterval = 500 * time.Millisecond
	maxCPUInterval     = 5 * time.Second
)

// GetCoreUsage diffs two snapshots of the kernel counters, `interval` apart.
func GetCoreUsage(ctx context.Context, interval time.Duration) ([]CoreUsage, error) {
	if interval <= 0 {
		interval = defaultCPUInterval
	}
	if interval > maxCPUInterval {
		interval = maxCPUInterval
	}

	before, err := cpu.TimesWithContext(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("first read of /proc/stat: %w", err)
	}

	// Not time.Sleep: that ignores cancellation, and this call holds the caller
	// for the whole window.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(interval):
	}

	after, err := cpu.TimesWithContext(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("second read of /proc/stat: %w", err)
	}

	if len(before) != len(after) {
		return nil, fmt.Errorf("core count changed between reads (%d -> %d)",
			len(before), len(after))
	}

	cores := make([]CoreUsage, 0, len(after))
	for i := range after {
		cores = append(cores, diffCoreTimes(before[i], after[i]))
	}
	return cores, nil
}

func diffCoreTimes(before, after cpu.TimesStat) CoreUsage {
	// Guest and GuestNice are excluded: the kernel already counts them inside
	// User and Nice, so adding them would inflate the denominator.
	total := func(t cpu.TimesStat) float64 {
		return t.User + t.System + t.Nice + t.Iowait +
			t.Irq + t.Softirq + t.Steal + t.Idle
	}

	elapsed := total(after) - total(before)
	if elapsed <= 0 {
		// Window too short, or the kernel clock did not advance.
		return CoreUsage{Core: after.CPU, IdlePercent: 100}
	}

	pct := func(b, a float64) float64 {
		return round2((a - b) / elapsed * 100)
	}

	c := CoreUsage{
		Core:          after.CPU,
		UserPercent:   pct(before.User, after.User),
		SystemPercent: pct(before.System, after.System),
		NicePercent:   pct(before.Nice, after.Nice),
		IRQPercent:    pct(before.Irq+before.Softirq, after.Irq+after.Softirq),
		StealPercent:  pct(before.Steal, after.Steal),
		IOWaitPercent: pct(before.Iowait, after.Iowait),
		IdlePercent:   pct(before.Idle, after.Idle),
	}
	c.TotalPercent = round2(c.UserPercent + c.SystemPercent +
		c.NicePercent + c.IRQPercent + c.StealPercent)

	return c
}
