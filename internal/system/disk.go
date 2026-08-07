package system

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

type DiskUsage struct {
	Device     string `json:"device"`
	Mountpoint string `json:"mountpoint"`
	Fstype     string `json:"fstype"`
	ReadOnly   bool   `json:"read_only"`

	Total       Bytes   `json:"total"`
	Used        Bytes   `json:"used"`
	Free        Bytes   `json:"free"`
	UsedPercent float64 `json:"used_percent"`

	// Inodes can run out independently of space. See the note below.
	InodesTotal       uint64  `json:"inodes_total,omitempty"`
	InodesUsed        uint64  `json:"inodes_used,omitempty"`
	InodesUsedPercent float64 `json:"inodes_used_percent,omitempty"`

	// Filled in when the query for THIS mountpoint failed,
	// without invalidating the others.
	Error string `json:"error,omitempty"`
}

type DiskStats struct {
	Filesystems  []DiskUsage `json:"filesystems"`
	SkippedCount int         `json:"skipped_count" jsonschema:"mountpoints discarded for being pseudo-filesystems, snaps or duplicates"`
	Warnings     []string    `json:"warnings,omitempty"`
}

// Filesystem types that never represent real storage the user would
// care about monitoring.
var ignoredFstypes = map[string]bool{
	"squashfs": true, "tmpfs": true, "devtmpfs": true, "overlay": true,
	"ramfs": true, "autofs": true, "iso9660": true, "fusectl": true,
	"configfs": true, "debugfs": true, "tracefs": true, "securityfs": true,
	"cgroup": true, "cgroup2": true, "bpf": true, "pstore": true,
	"hugetlbfs": true, "mqueue": true, "nsfs": true, "binfmt_misc": true,
}

// Paths that are an implementation detail of some other tool.
var ignoredPrefixes = []string{
	"/snap/", "/var/lib/docker/", "/var/lib/containers/", "/var/lib/kubelet/",
}

func GetDiskStats(ctx context.Context, includeAll bool) (DiskStats, error) {
	parts, err := disk.PartitionsWithContext(ctx, includeAll)
	if err != nil {
		return DiskStats{}, fmt.Errorf("listing mountpoints: %w", err)
	}

	var (
		selected []disk.PartitionStat
		skipped  int
		seen     = make(map[string]bool)
	)
	for _, p := range parts {
		if !includeAll && !isInteresting(p, seen) {
			skipped++
			continue
		}
		selected = append(selected, p)
	}

	// We pre-allocate the slice at its final size. Each goroutine writes
	// EXCLUSIVELY to its own index, so there is no concurrent write to the
	// same address — which is why no mutex is needed.
	// This is the idiomatic "parallel map" pattern in Go.
	results := make([]DiskUsage, len(selected))

	var wg sync.WaitGroup
	for i, p := range selected {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = usageFor(ctx, p)
		}()
	}
	wg.Wait()

	stats := DiskStats{Filesystems: results, SkippedCount: skipped}

	for _, fs := range results {
		if fs.Error != "" {
			stats.Warnings = append(stats.Warnings,
				fmt.Sprintf("%s: %s", fs.Mountpoint, fs.Error))
		}
	}

	// Fullest first: whatever needs attention shows up at the top.
	slices.SortFunc(stats.Filesystems, func(a, b DiskUsage) int {
		switch {
		case a.UsedPercent > b.UsedPercent:
			return -1
		case a.UsedPercent < b.UsedPercent:
			return 1
		default:
			return strings.Compare(a.Mountpoint, b.Mountpoint)
		}
	})

	return stats, nil
}

func isInteresting(p disk.PartitionStat, seen map[string]bool) bool {
	if ignoredFstypes[p.Fstype] {
		return false
	}
	if strings.HasPrefix(p.Fstype, "fuse.") {
		return false
	}
	for _, prefix := range ignoredPrefixes {
		if strings.HasPrefix(p.Mountpoint, prefix) {
			return false
		}
	}
	// Bind mounts and subvolumes make the same device show up several
	// times with the same space. We keep the first occurrence.
	if p.Device != "" && p.Device != "none" {
		if seen[p.Device] {
			return false
		}
		seen[p.Device] = true
	}
	return true
}

func usageFor(ctx context.Context, p disk.PartitionStat) DiskUsage {
	u := DiskUsage{
		Device:     p.Device,
		Mountpoint: p.Mountpoint,
		Fstype:     p.Fstype,
		ReadOnly:   slices.Contains(p.Opts, "ro"),
	}

	stat, err := usageWithTimeout(ctx, p.Mountpoint, 2*time.Second)
	if err != nil {
		u.Error = err.Error()
		return u
	}

	u.Total = newBytes(stat.Total)
	u.Used = newBytes(stat.Used)
	u.Free = newBytes(stat.Free)
	u.UsedPercent = round2(stat.UsedPercent)
	u.InodesTotal = stat.InodesTotal
	u.InodesUsed = stat.InodesUsed
	u.InodesUsedPercent = round2(stat.InodesUsedPercent)

	return u
}

// disk.Usage performs the statfs syscall. On a network mount (NFS, SMB,
// SSHFS) whose server is down, that syscall can hang for MINUTES.
//
// And it does NOT honor context cancellation: what is stuck is the kernel,
// not the Go code. Passing ctx accomplishes nothing.
//
// The way out is to abandon the WAIT without being able to abandon the
// goroutine.
func usageWithTimeout(ctx context.Context, path string, timeout time.Duration) (*disk.UsageStat, error) {
	type result struct {
		stat *disk.UsageStat
		err  error
	}

	// A buffer of size 1 is ESSENTIAL. If the channel were unbuffered and
	// we had already given up on the timeout, the goroutine would block
	// forever trying to send — a permanent leak. With the buffer, it drops
	// off the result, nobody reads it, and the GC collects everything later.
	ch := make(chan result, 1)

	go func() {
		stat, err := disk.UsageWithContext(ctx, path)
		ch <- result{stat, err}
	}()

	select {
	case r := <-ch:
		return r.stat, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout of %s while querying the mountpoint "+
			"(network mount unavailable?)", timeout)
	}
}

// CompactBytes formats in the style of `df -h`: one decimal place below 10,
// none above. Binary units, single-letter suffix.
//
// Exported (capital initial) because the mcp package needs it to render
// the table — it is exactly the package boundary you just touched.
func CompactBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	if v < 10 {
		return fmt.Sprintf("%.1f%c", v, "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.0f%c", v, "KMGTPE"[exp])
}
