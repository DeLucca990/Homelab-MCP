package system

import (
	"context"
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v4/host"
)

type HostInfo struct {
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	Platform        string `json:"platform"`
	PlatformVersion string `json:"platform_version"`
	KernelVersion   string `json:"kernel_version"`
	Architecture    string `json:"architecture"`
	Processes       uint64 `json:"processes"`
	UptimeSeconds   uint64 `json:"uptime_seconds"`
	UptimeHuman     string `json:"uptime_human"`
	BootTime        string `json:"boot_time"`
}

func GetHostInfo(ctx context.Context) (HostInfo, error) {
	info, err := host.InfoWithContext(ctx)
	if err != nil {
		return HostInfo{}, fmt.Errorf("getting host info: %w", err)
	}

	uptime := time.Duration(info.Uptime) * time.Second

	return HostInfo{
		Hostname:        info.Hostname,
		OS:              info.OS,
		Platform:        info.Platform,
		PlatformVersion: info.PlatformVersion,
		KernelVersion:   info.KernelVersion,
		Architecture:    info.KernelArch,
		Processes:       info.Procs,
		UptimeSeconds:   info.Uptime,
		UptimeHuman:     uptime.String(),
		BootTime:        time.Unix(int64(info.BootTime), 0).Format(time.RFC3339),
	}, nil
}
