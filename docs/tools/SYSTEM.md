# System tools

Five tools, all read-only, all registered unconditionally. They answer *"is this
machine healthy?"* without an SSH session.

Data comes from the kernel through
[gopsutil](https://github.com/shirou/gopsutil), except `system_service_status`,
which shells out to `systemctl`. Design notes:
[docs/modules/system.md](../modules/system.md).

| Tool | Input | What it answers |
| --- | --- | --- |
| `system_host_info` | — | hostname, OS, kernel version, architecture, uptime, process count |
| `system_cpu_cores` | `sample_ms` | per-core usage split into user / kernel / nice / IRQ / I/O wait |
| `system_memory_stats` | — | RAM and swap usage |
| `system_disk_usage` | `include_all` | disk usage per mountpoint, fullest first, plus inode usage |
| `system_service_status` | `units`, `include_all` | systemd unit state, worst first |

---

## `system_host_info`

No parameters. Returns hostname, operating system, kernel version, architecture,
uptime and process count.

---

## `system_cpu_cores`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `sample_ms` | integer | `500` | sampling window, max `5000` |

Per-core usage broken down into user, kernel, nice, interrupt and I/O wait — the
same breakdown htop shows per core. **Takes about 500ms**, because CPU usage is
a delta between two readings of `/proc/stat` and there is no way to have one
instantly.

```
cpu0    30.0%  (usr 16.0  sys 14.0  io 0.0)
cpu1    18.0%  (usr 10.0  sys 8.0  io 0.0)
cpu2    12.2%  (usr 8.2  sys 4.1  io 0.0)
cpu3    10.0%  (usr 4.0  sys 6.0  io 0.0)
```

The text shows the three that matter at a glance; nice, IRQ and steal are in the
structured JSON.

---

## `system_memory_stats`

No parameters. RAM and swap, rendered like `free -h`:

```
             total        used        free      shared  buff/cache   available
Mem:          24Gi        16Gi       272Mi          0B        4.2Gi       7.7Gi
Swap:        3.0Gi       1.7Gi       1.3Gi
```

**Read `available_bytes` and `used_percent`, never `free_bytes`.** Linux keeps
idle RAM occupied with disk cache, so a low `free` is normal and says nothing
about memory pressure. The tool description tells the model this, because it is
the single most common misreading of these numbers.

---

## `system_disk_usage`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `include_all` | boolean | `false` | include pseudo-filesystems, snaps and duplicate mounts |

Usage per mountpoint, fullest first, in `df -h` shape:

```
Filesystem      Size  Used  Avail  Use%  Mounted on
/dev/sda2       916G  871G    45G   95%  /
/dev/sda1       511M   34M   478M    7%  /boot/efi

warning: / is at 92% inode usage (54983168 of 59768832) — it can fail with
"no space left on device" even with free space

(14 mounts filtered out; use include_all to see them)
```

Two things a plain `df -h` will not tell you:

- **Inode exhaustion.** A filesystem can fail with `no space left on device`
  while still showing free bytes. Above 80% inode usage this warns.
- **Hung network mounts.** `statfs` on an unreachable NFS/SMB mount blocks in
  the kernel and ignores context cancellation. Each mountpoint gets a 2s
  timeout, so one dead mount degrades to a warning instead of hanging the call.

By default snap packages and container layers are filtered out — they show as
100% full without that indicating anything.

---

## `system_service_status`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `units` | string[] | — | specific unit names; when empty, every unit is scanned |
| `include_all` | boolean | `false` | also return healthy units |

**Linux only** — errors on hosts without systemd. By default it scans everything
and reports only what needs attention (failed, stuck starting, restarting),
worst first.

```
UNIT               LOAD    ACTIVE      SUB     RESTARTS  FOR
jellyfin.service   loaded  failed      failed         5  1m
sonarr.service     loaded  failed      failed         0  2m
nginx.service      loaded  activating  start          0  2m

warning: jellyfin.service failed — was killed by the OOM killer, after 5 restarts
(systemd stopped retrying)

warning: sonarr.service failed — exited with code 3

warning: nginx.service has been starting for 122s without reaching active — it may be stuck

(41 healthy units omitted; use include_all to see them)
```

On a healthy host it answers plainly rather than returning an empty list:

```
no services needing attention (41 units, 11 active, 0 failed)
```

**The crash loop this exists to catch.** A service restarting every few seconds
is `active` in any point-in-time check. `RESTARTS` and `FOR` together tell "up"
apart from "up for 9 seconds after 800 restarts", and the warning names the
reason systemd recorded — including OOM kills.
