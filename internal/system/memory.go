package system

import (
	"context"
	"fmt"

	"github.com/shirou/gopsutil/v4/mem"
)

type Bytes struct {
	Bytes uint64 `json:"bytes"`
	Human string `json:"human"`
}

func newBytes(b uint64) Bytes {
	return Bytes{Bytes: b, Human: humanBytes(b)}
}

type MemoryStats struct {
	Total            Bytes   `json:"total"`
	Available        Bytes   `json:"available" jsonschema:"memory a new application could allocate without swapping — this is the number that indicates memory pressure"`
	Used             Bytes   `json:"used" jsonschema:"total minus available; excludes reclaimable cache"`
	Free             Bytes   `json:"free" jsonschema:"untouched RAM; usually low even on healthy servers, since the kernel uses the leftovers as cache"`
	Cached           Bytes   `json:"cached" jsonschema:"page cache, instantly reclaimable on demand"`
	Buffers          Bytes   `json:"buffers"`
	Shared           Bytes   `json:"shared"`
	BuffCache        Bytes   `json:"buff_cache" jsonschema:"buffers + cached + reclaimable slab; this is free's buff/cache column"`
	UsedPercent      float64 `json:"used_percent"`
	AvailablePercent float64 `json:"available_percent"`

	Swap SwapStats `json:"swap"`

	// Partial collections are reported here instead of silently
	// returning zero.
	Warnings []string `json:"warnings,omitempty"`
}

type SwapStats struct {
	Configured  bool    `json:"configured"`
	Total       Bytes   `json:"total"`
	Used        Bytes   `json:"used"`
	Free        Bytes   `json:"free"`
	UsedPercent float64 `json:"used_percent"`
}

func GetMemoryStats(ctx context.Context) (MemoryStats, error) {
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return MemoryStats{}, fmt.Errorf("reading virtual memory: %w", err)
	}

	stats := MemoryStats{
		Total:     newBytes(vm.Total),
		Available: newBytes(vm.Available),
		Free:      newBytes(vm.Free),
		Cached:    newBytes(vm.Cached),
		Buffers:   newBytes(vm.Buffers),
		Shared:    newBytes(vm.Shared),
		BuffCache: newBytes(vm.Buffers + vm.Cached + vm.Sreclaimable),
	}

	// We compute the percentages ourselves instead of using vm.UsedPercent:
	// the definition of that field has changed across gopsutil versions, and
	// here we want the guarantee that "used" means "total minus available".
	if vm.Total > 0 {
		used := vm.Total - vm.Available
		stats.Used = newBytes(used)
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
			Total:       newBytes(sw.Total),
			Used:        newBytes(sw.Used),
			Free:        newBytes(sw.Free),
			UsedPercent: round2(sw.UsedPercent),
		}
	}

	return stats, nil
}
