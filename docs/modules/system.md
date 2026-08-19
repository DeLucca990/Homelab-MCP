# The system module

`internal/system/` (host, CPU, memory, disk) and `internal/services/` (systemd
units). Tool reference: [tools/SYSTEM.md](../tools/SYSTEM.md).

These are the only collectors that read the machine directly rather than talking
to a service, and the only ones that run a binary.

---

## Reading the kernel

Everything except systemd comes from
[gopsutil](https://github.com/shirou/gopsutil), which is one of this module's two
direct dependencies. The collectors return plain structs; nothing here imports
anything from `internal/mcp`.

### A dead mount must not take the call with it

`statfs` on an unreachable NFS or SMB mount blocks **in the kernel**. It ignores
context cancellation — a `ctx` deadline cannot interrupt a syscall that is not
looking at it — so the usual pattern of passing a context down does nothing.

Each mountpoint is therefore queried on its own with a 2s budget, and a mount
that does not answer in time becomes a warning on that row rather than a call
that never returns. One dead mount degrades the answer; it does not withhold it.

### Inode exhaustion is a separate axis

A filesystem can fail with `no space left on device` while `df` shows free bytes,
because it ran out of inodes instead. Nothing in a byte-oriented listing hints at
this, so inode usage is collected alongside and warned on above 80%.

This is also the clearest example of the rule in
[ARCHITECTURE §1](../ARCHITECTURE.md#1-layout): the warning is computed in the
collector, not in the renderer, so a client reading only `structuredContent`
still gets it.

### Filtering is opinionated, and reversible

Pseudo-filesystems, snap packages and container layers are hidden by default.
They report 100% full as a matter of course, and a listing where three quarters
of the rows are permanently at 100% trains the reader to ignore the column that
matters. `include_all` brings them back.

---

## Running `systemctl`

`system_service_status` is the one place a model-supplied string reaches a
process's argv. Three properties keep that safe.

**No shell.** `os/exec` calls `execve` with an argument vector. There is no `sh
-c` anywhere, so shell metacharacters in any input are inert — there is no string
a caller can supply that becomes a second command.

**Fixed verbs.** Only `list-units` and `show` are ever invoked. Both are
read-only and neither is caller-selectable; the caller chooses *which units*, not
*what to do*.

**Validated unit names.** Shell injection being impossible is not the end of it:
`systemctl` parses its own arguments, and a name beginning with `-` would be read
as an option. `--host=` in particular makes it dial out over SSH. So names are
checked before anything runs:

```go
var validUnitName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@:\\-]{0,255}$`)
```

The leading character class is the point — the rest is generosity.

**A 5s timeout**, because a wedged systemd would otherwise hold the tool call
open indefinitely.

---

## The crash loop

A service restarting every few seconds is `active` at almost any instant you look
at it. `systemctl status` shows it green. This is the failure the module exists
to name, and it takes two numbers rather than one:

- `RESTARTS` — how many times systemd has restarted the unit
- `FOR` — how long it has held its *current* state

Either alone is misleading. Together, "active, 800 restarts, up for 9 seconds"
cannot be read as healthy. The reason systemd recorded is reported alongside,
including OOM kills, so the warning says *why* rather than only *that*.

The same shape appears in [the Docker module](docker.md), for the same reason:
point-in-time state is not health.
