package mcp

import (
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/containers"
)

func registerTools(s *sdk.Server) {
	// system host tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_host_info",
		Annotations: &sdk.ToolAnnotations{
			Title:        "System Host Info",
			ReadOnlyHint: true,
		},
		Description: "Returns general server information: hostname, operating system, kernel version, architecture and uptime.",
	}, handleHostInfo)

	// system cpu cores
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_cpu_cores",
		Annotations: &sdk.ToolAnnotations{
			Title:        "System CPU Cores",
			ReadOnlyHint: true,
		},
		Description: "Returns the detailed usage of each CPU core individually, " +
			"broken down into user, kernel, nice, interrupt and I/O wait time — " +
			"the same breakdown htop shows per core. Takes about 500ms.",
	}, handleCoreUsage)

	// system memory tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_memory_stats",
		Annotations: &sdk.ToolAnnotations{
			Title:        "System Memory Stats",
			ReadOnlyHint: true,
		},
		Description: "Returns the server's RAM and swap usage. " +
			"To assess memory pressure use 'available_bytes' and 'used_percent', " +
			"never 'free_bytes' — Linux keeps idle RAM occupied with disk cache, " +
			"so a low 'free_bytes' is normal and does not indicate a problem. Immediate response.",
	}, handleMemoryStats)

	// system disk tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_disk_usage",
		Annotations: &sdk.ToolAnnotations{
			Title:        "System Disk Usage",
			ReadOnlyHint: true,
		},
		Description: "Returns disk space usage per mountpoint, sorted from " +
			"fullest to emptiest. By default it filters out pseudo-filesystems, snap packages " +
			"and container layers, which show up as 100% full without that indicating a problem. " +
			"Also includes inode usage: a disk can become unusable from inode exhaustion " +
			"even with plenty of free bytes.",
	}, handleDiskStats)

	// systemd services tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_service_status",
		Annotations: &sdk.ToolAnnotations{
			Title:        "System Service Status",
			ReadOnlyHint: true,
		},
		Description: "Returns the state of systemd service units — whether the services on " +
			"this server are running. By default it scans every unit and reports only those " +
			"needing attention (failed, stuck starting, or restarting), worst first; pass " +
			"'units' to ask about specific ones by name. Reports the restart count, which is " +
			"what distinguishes a service that is genuinely running from one that is " +
			"crash-looping — the latter reads as active in any point-in-time check. " +
			"Linux only; errors on hosts without systemd.",
	}, handleServiceStatus)

	// docker containers tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "docker_container_status",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Docker Containe Status",
			ReadOnlyHint: true,
		},
		Description: "Returns the state of Docker containers, worst first. By default it " +
			"reports running containers plus anything broken, and hides containers that " +
			"stopped cleanly; pass 'names' to ask about specific ones. Beyond what " +
			"'docker ps' shows, it reports healthcheck results, restart counts, exit codes, " +
			"and whether a container was killed by the OOM killer for exceeding its memory " +
			"limit — the usual cause of a container that keeps dying for no visible reason. " +
			"Requires access to the docker socket.",
	}, handleContainerStatus)

	// docker logs tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "docker_container_logs",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Docker Container Logs",
			ReadOnlyHint: true,
		},
		Description: "Returns what a container has written to stdout and stderr, interleaved " +
			"in order, most recent lines by default. This is the follow-up to any finding from " +
			"docker_container_status — an OOM kill, a failing healthcheck or a restart loop " +
			"tells you a container is broken, and the logs tell you why. Note that most images " +
			"log to stdout, which the daemon captures and which therefore exists nowhere in the " +
			"container's own filesystem: reading it with a shell command would find nothing. " +
			"Read-only.",
	}, handleLogs)

	// docker exec + restart tools
	if allowed := containers.ActionAllowlist(); len(allowed) > 0 { // initialization statment condition - if <statement>; <condição> {}
		sdk.AddTool(s, &sdk.Tool{
			Name: "docker_container_exec",
			Annotations: &sdk.ToolAnnotations{
				Title:           "Run a command inside a container",
				ReadOnlyHint:    false,
				DestructiveHint: ptr(true),
				OpenWorldHint:   ptr(false),
			},
			Description: "Runs a command inside one of the containers this server is permitted " +
				"to reach (" + strings.Join(allowed, ", ") + ") and returns its stdout, stderr " +
				"and exit code. ...",
		}, handleExec)

		sdk.AddTool(s, &sdk.Tool{
			Name: "docker_container_restart",
			Annotations: &sdk.ToolAnnotations{
				Title:           "Restart a container",
				ReadOnlyHint:    false,
				DestructiveHint: ptr(true),
				OpenWorldHint:   ptr(false),
				IdempotentHint:  true,
			},
			Description: "Restarts one of the containers this server is permitted to restart (" +
				strings.Join(allowed, ", ") + "), then waits and reports whether it actually came " +
				"back up. ...",
		}, handleRestart)
	}
}
