package system

import "fmt"

// formatBytes renders b in binary units with a single-letter suffix: one
// decimal place below 10, none above — the density of `df -h`.
//
// unitSuffix is appended after the unit letter, which is the only thing that
// separates the two styles the tools render: `df -h` writes 4.0G, `free -h`
// writes 4.0Gi. Values under 1 KiB are plain bytes in both.
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

// CompactBytes formats in the style of `df -h`: 916G, 4.0G, 511M.
func CompactBytes(b uint64) string { return formatBytes(b, "") }

// IECBytes formats in the style of `free -h`: 24Gi, 4.0Gi, 272Mi.
func IECBytes(b uint64) string { return formatBytes(b, "i") }

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
