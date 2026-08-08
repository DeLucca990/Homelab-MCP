package system

import "fmt"

// Binary units with a single-letter suffix, one decimal below 10 and none
// above. unitSuffix is all that separates the two styles the tools render:
// `df -h` writes 4.0G, `free -h` writes 4.0Gi.
func formatBytes(b uint64, unitSuffix string) string {
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
		return fmt.Sprintf("%.1f%c%s", v, "KMGTPE"[exp], unitSuffix)
	}
	return fmt.Sprintf("%.0f%c%s", v, "KMGTPE"[exp], unitSuffix)
}

// CompactBytes is `df -h` style: 916G, 4.0G, 511M.
func CompactBytes(b uint64) string { return formatBytes(b, "") }

// IECBytes is `free -h` style: 24Gi, 4.0Gi, 272Mi.
func IECBytes(b uint64) string { return formatBytes(b, "i") }

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
