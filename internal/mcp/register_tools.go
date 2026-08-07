package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type emptyInput struct{}

func registerTools(s *sdk.Server) {
	// system host tool
	sdk.AddTool(s, &sdk.Tool{
		Name:        "system_host_info",
		Description: "Returns general server information: hostname, operating system, kernel version, architecture and uptime.",
	}, handleHostInfo)

	// system cpu cores
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_cpu_cores",
		Description: "Returns the detailed usage of each CPU core individually, " +
			"broken down into user, kernel, nice, interrupt and I/O wait time — " +
			"the same breakdown htop shows per core. Takes about 500ms.",
	}, handleCoreUsage)

	// system memory tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_memory_stats",
		Description: "Returns the server's RAM and swap usage. " +
			"To assess memory pressure use 'available_bytes' and 'used_percent', " +
			"never 'free_bytes' — Linux keeps idle RAM occupied with disk cache, " +
			"so a low 'free_bytes' is normal and does not indicate a problem. Immediate response.",
	}, handleMemoryStats)

	// system disk tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_disk_usage",
		Description: "Returns disk space usage per mountpoint, sorted from " +
			"fullest to emptiest. By default it filters out pseudo-filesystems, snap packages " +
			"and container layers, which show up as 100% full without that indicating a problem. " +
			"Also includes inode usage: a disk can become unusable from inode exhaustion " +
			"even with plenty of free bytes.",
	}, handleDiskStats)

	// systemd services tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_service_status",
		Description: "Returns the state of systemd service units — whether the services on " +
			"this server are running. By default it scans every unit and reports only those " +
			"needing attention (failed, stuck starting, or restarting), worst first; pass " +
			"'units' to ask about specific ones by name. Reports the restart count, which is " +
			"what distinguishes a service that is genuinely running from one that is " +
			"crash-looping — the latter reads as active in any point-in-time check. " +
			"Linux only; errors on hosts without systemd.",
	}, handleServiceStatus)
}
