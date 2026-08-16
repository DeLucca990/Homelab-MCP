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

	TotalBytes  uint64  `json:"total_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`
	FreeBytes   uint64  `json:"free_bytes"`
	UsedPercent float64 `json:"used_percent"`

	// Inodes can run out independently of space.
	InodesTotal       uint64  `json:"inodes_total,omitempty"`
	InodesUsed        uint64  `json:"inodes_used,omitempty"`
	InodesUsedPercent float64 `json:"inodes_used_percent,omitempty"`

	// Set when the query for THIS mountpoint failed, without invalidating
	// the others.
	Error string `json:"error,omitempty"`
}

type DiskStats struct {
	Filesystems  []DiskUsage `json:"filesystems"`
	SkippedCount int         `json:"skipped_count" jsonschema:"mountpoints discarded for being pseudo-filesystems, snaps or duplicates"`
	Warnings     []string    `json:"warnings,omitempty"`
}

// Filesystem types that never represent real storage worth monitoring.
var ignoredFstypes = map[string]bool{
	"squashfs": true, "tmpfs": true, "devtmpfs": true, "devfs": true, "overlay": true,
	"ramfs": true, "autofs": true, "iso9660": true, "fusectl": true,
	"configfs": true, "debugfs": true, "tracefs": true, "securityfs": true,
	"cgroup": true, "cgroup2": true, "bpf": true, "pstore": true,
	"hugetlbfs": true, "mqueue": true, "nsfs": true, "binfmt_misc": true,
}

// Paths that are an implementation detail of some other tool.
var ignoredPrefixes = []string{
	"/snap/", "/var/lib/docker/", "/var/lib/containers/", "/var/lib/kubelet/",
}

// Above this inode percentage we warn explicitly: `df -h` hides it completely.
const inodeWarnThreshold = 80.0

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

	// One goroutine per mountpoint, each writing to its own index: statfs on a
	// dead network mount is slow, and serially they would add up.
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

	// Built here rather than in the renderer so a client reading only
	// structuredContent sees them too, and after the sort so their order
	// matches the table's.
	for _, fs := range stats.Filesystems {
		if fs.Error != "" {
			stats.Warnings = append(stats.Warnings,
				fmt.Sprintf("%s: %s", fs.Mountpoint, fs.Error))
			continue
		}
		if fs.InodesUsedPercent >= inodeWarnThreshold {
			stats.Warnings = append(stats.Warnings, fmt.Sprintf(
				"%s is at %.0f%% inode usage (%d of %d) — it can fail with "+
					`"no space left on device" even with free space`,
				fs.Mountpoint, fs.InodesUsedPercent, fs.InodesUsed, fs.InodesTotal))
		}
	}

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
	// Bind mounts and subvolumes repeat the same device with the same space;
	// keep the first occurrence.
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

	u.TotalBytes = stat.Total
	u.UsedBytes = stat.Used
	u.FreeBytes = stat.Free
	u.UsedPercent = round2(stat.UsedPercent)
	u.InodesTotal = stat.InodesTotal
	u.InodesUsed = stat.InodesUsed
	u.InodesUsedPercent = round2(stat.InodesUsedPercent)

	return u
}

// disk.Usage performs statfs, which on a network mount (NFS, SMB, SSHFS) whose
// server is down can hang for MINUTES and does not honour ctx — what is stuck
// is the kernel, not the Go code. So we abandon the wait, not the goroutine.
func usageWithTimeout(ctx context.Context, path string, timeout time.Duration) (*disk.UsageStat, error) {
	type result struct {
		stat *disk.UsageStat
		err  error
	}

	// Buffered so the abandoned goroutine can send and exit instead of
	// blocking forever on a receiver that gave up.
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
