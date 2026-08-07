package system

import (
	"context"
	"fmt"

	"github.com/shirou/gopsutil/v4/mem"
)

// Sizes are raw byte counts. The `_bytes` suffix carries the unit so the
// numbers need no human-readable companion field — the tool renders that
// once, in text, instead of duplicating every value in the JSON.
type MemoryStats struct {
	TotalBytes       uint64  `json:"total_bytes"`
	AvailableBytes   uint64  `json:"available_bytes" jsonschema:"memory a new application could allocate without swapping — this is the number that indicates memory pressure"`
	UsedBytes        uint64  `json:"used_bytes" jsonschema:"total minus available; excludes reclaimable cache"`
	FreeBytes        uint64  `json:"free_bytes" jsonschema:"untouched RAM; usually low even on healthy servers, since the kernel uses the leftovers as cache"`
	CachedBytes      uint64  `json:"cached_bytes" jsonschema:"page cache, instantly reclaimable on demand"`
	BuffersBytes     uint64  `json:"buffers_bytes"`
	SharedBytes      uint64  `json:"shared_bytes"`
	BuffCacheBytes   uint64  `json:"buff_cache_bytes" jsonschema:"buffers + cached + reclaimable slab; this is free's buff/cache column"`
	UsedPercent      float64 `json:"used_percent"`
	AvailablePercent float64 `json:"available_percent"`

	Swap SwapStats `json:"swap"`

	// Partial collections are reported here instead of silently
	// returning zero.
	Warnings []string `json:"warnings,omitempty"`
}

type SwapStats struct {
	Configured  bool    `json:"configured"`
	TotalBytes  uint64  `json:"total_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`
	FreeBytes   uint64  `json:"free_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

func GetMemoryStats(ctx context.Context) (MemoryStats, error) {
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return MemoryStats{}, fmt.Errorf("reading virtual memory: %w", err)
	}

	stats := MemoryStats{
		TotalBytes:     vm.Total,
		AvailableBytes: vm.Available,
		FreeBytes:      vm.Free,
		CachedBytes:    vm.Cached,
		BuffersBytes:   vm.Buffers,
		SharedBytes:    vm.Shared,
		BuffCacheBytes: vm.Buffers + vm.Cached + vm.Sreclaimable,
	}

	// We compute the percentages ourselves instead of using vm.UsedPercent:
	// the definition of that field has changed across gopsutil versions, and
	// here we want the guarantee that "used" means "total minus available".
	if vm.Total > 0 {
		used := vm.Total - vm.Available
		stats.UsedBytes = used
		stats.UsedPercent = round2(float64(used) / float64(vm.Total) * 100)
		stats.AvailablePercent = round2(float64(vm.Available) / float64(vm.Total) * 100)
	}

	sw, err := mem.SwapMemoryWithContext(ctx)
	switch {
	case err != nil:
		// A failure reading swap doesn't invalidate the RAM data — report it
		// and carry on.
		stats.Warnings = append(stats.Warnings,
			"could not read swap statistics: "+err.Error())
	case sw.Total == 0:
		// No swap is a valid configuration, not an error.
		stats.Swap.Configured = false
	default:
		stats.Swap = SwapStats{
			Configured:  true,
			TotalBytes:  sw.Total,
			UsedBytes:   sw.Used,
			FreeBytes:   sw.Free,
			UsedPercent: round2(sw.UsedPercent),
		}
	}

	return stats, nil
}
