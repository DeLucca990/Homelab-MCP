# The Docker module

`internal/containers/`, over the Engine API on the unix socket. Tool reference:
[tools/DOCKER.md](../../tools/DOCKER.md).

This is the module with the widest blast radius — the socket is root-equivalent —
and it is the one whose action tools are off by default.

---

## Talking to the daemon

The daemon speaks plain HTTP over `/var/run/docker.sock`, so a stock `net/http`
client with a custom dialer reaches it:

```go
Transport: &http.Transport{
    DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
        var d net.Dialer
        return d.DialContext(ctx, "unix", path)
    },
},
```

**Why not the Docker SDK.** It would be the largest dependency in a module with
two direct ones, to save a dialer and a handful of struct tags. The API surface
used here is small and stable: list, inspect, logs, exec, restart.

`DOCKER_HOST` is honoured when it names a `unix://` path, so rootless installs —
which put the socket under `$XDG_RUNTIME_DIR` — work without configuration.

The host in the URL (`http://docker`) is meaningless: the dialer always goes to
the socket. `net/http` just requires a syntactically valid one.

A known wart: `newClient` builds a fresh `http.Client` per call, where
`internal/radarr` shares one for the process. Each abandoned Transport keeps its
idle connections until they time out. Over a unix socket the cost is small, but
the two modules should agree.

---

## Point-in-time state is not health

`docker ps` answers "what is the state right now", which is the wrong question
three times over. `GetContainerStatus` inspects each container — in parallel, one
goroutine per container, because the latency adds up serially — and reports what
the listing leaves out:

- **`OOMKilled`.** A container the kernel killed for exceeding its memory limit
  shows as `Exited (137)`, indistinguishable from an application that exited
  137 on its own. The flag says which it was, and the limit it hit is reported
  next to it.
- **Healthcheck result and failing streak.** `running` plus `unhealthy` means the
  process is up and the service behind it is not.
- **`RestartCount` beside `StateForSeconds`.** Up for 4 seconds with 11 restarts
  is a crash loop wearing the word "running".

Containers are sorted by a `severity` function, worst first, so whatever is
broken is read before whatever is fine. Cleanly-exited containers — the residue
of one-shot runs a homelab accumulates — are hidden unless asked for, because a
listing three quarters full of expected noise is a listing nobody reads.

Warnings are built in `buildWarnings`, in the collector, so both result channels
carry them.

---

## The three layers

Between the model and a container that can be changed:

### 1. The allowlist — what is reachable at all

`HOMELAB_MCP_ALLOW_CONTAINER_NAMES`, a comma-separated list of container names.
Empty means the action tools are **never registered**, and a model cannot call a
tool that does not exist.

This is the only layer that **cannot be bypassed by a confused client, a client
that auto-approves everything, or a model under prompt injection.** The other two
depend on something outside this server behaving; this one does not.

One list covers every action, deliberately. A shell inside a container already
carries the power to take that container down, so "may run commands in X" and
"may restart X" are not separable grants — splitting them would advertise a
distinction the runtime does not enforce.

The list is also interpolated into each tool's description, so the model is told
which containers it may name instead of discovering it by being refused.

### 2. Human confirmation

The shared flow in
[ARCHITECTURE §3](../ARCHITECTURE.md#3-waiting-for-a-user-response). The message
shows the command verbatim, and flags when the command is a shell — because
`["sh","-c","rm -rf /config"]` and `["ls","/config"]` are the same shape of
request and very different outcomes.

### 3. The fingerprint

`containers.Fingerprint(container, argv)`, checked on the retry. See
[ARCHITECTURE §4.2](../ARCHITECTURE.md#42-the-fingerprint) for what it proves,
and for the one known collision: `docker_container_restart` uses the synthetic
argv `["restart"]`, which hashes identically to an exec of the literal command
`restart` in the same container.

---

## No caller-supplied string reaches a request path

Every Docker call resolves the name against the daemon's own listing first and
then interpolates **the id Docker returned**:

```go
matched := matchNames(summaries, []string{container})
id := matched[0].ID
```

A caller-supplied string is only ever compared, never used to build a URL. This
is why filtering by name happens in Go rather than through the API's own filter
parameter.

---

## Reading a multiplexed stream

Without a TTY, Docker frames exec and log output: an 8-byte header — stream type,
three padding bytes, big-endian payload length — followed by that many bytes.
`readFrames` walks it and hands each chunk to a callback, which is what lets exec
split stdout from stderr while the log reader keeps them interleaved in order.

Both are capped at 16 KiB, and truncation is reported rather than silent. What is
kept is the **beginning** of the output, and the result says so — a truncated tail
would otherwise read as a complete answer.

---

## Restart: waiting for the answer

Docker reports a container as `running` the instant the process spawns. A single
check after a restart would pass a container that dies a second later — which is
precisely the failure the tool exists to catch.

`waitUntilSettled` polls every 500ms for up to 15s and requires the container to
hold `running` for a **3s stable window**. It also waits out a healthcheck
reporting `starting`, rather than calling the restart good before the check has a
verdict. `exited` or `dead` short-circuits immediately: it came up and fell over.

The reported `came_back` is therefore an observation, not an assumption.
