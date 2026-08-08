# Architecture

How this server exposes tools, how it stops mid-call to ask the user for
permission, and what the approval fingerprint does and does not protect.

Written against `github.com/modelcontextprotocol/go-sdk` **v1.7.0**. The
multi-round-trip behaviour described in §3 is SDK- and protocol-version
sensitive; if you upgrade the SDK, re-read that section against the new code.

---

## 1. Layout

```
cmd/server/main.go        stdio transport, signal handling
internal/mcp/             protocol layer — registration, handlers, rendering, confirmation
internal/system/          host, CPU, memory, disk            (gopsutil)
internal/services/        systemd units                      (systemctl)
internal/containers/      docker                             (Engine API over the unix socket)
```

One rule holds the layering together: **`internal/mcp` never touches the OS, and
the collectors never know MCP exists.** The collectors return plain structs; the
protocol layer decides how to present them.

That rule has one consequence worth stating on its own, because it was
originally violated and the bug it caused was invisible: **warnings are computed
in the collector, not in the renderer.** An inode warning that exists only in
the rendered table is invisible to any client that reads `structuredContent`
instead of `content` — which is half the protocol. If a fact is worth telling
the user, it belongs in the struct.

---

## 2. How a tool is served

### Registration

Everything is registered in `internal/mcp/register.go` through the SDK's
generic helper:

```go
sdk.AddTool(s, &sdk.Tool{
    Name:        "system_disk_usage",
    Annotations: &sdk.ToolAnnotations{Title: "System Disk Usage", ReadOnlyHint: true},
    Description: "...",
}, handleDiskStats)
```

`AddTool` is generic over the handler's input and output types. From those two
Go types it generates the tool's **`inputSchema` and `outputSchema`** by
reflection, so the schema the model sees is derived from the struct — it cannot
drift from the code. `jsonschema:"..."` struct tags become field descriptions:

```go
type diskInput struct {
    IncludeAll bool `json:"include_all,omitempty" jsonschema:"if true, includes pseudo-filesystems, snaps and duplicate mounts that are normally filtered out"`
}
```

Those tags are the only documentation the model gets about a parameter. Treat
them as prompt text, not as comments.

### Handler shape

Every handler has the same signature:

```go
func(ctx context.Context, req *sdk.CallToolRequest, in In) (*sdk.CallToolResult, Out, error)
```

The two return values feed the two channels of a tool result:

| Return | Becomes | Read by |
|---|---|---|
| `Out` (the struct) | `structuredContent` | clients that parse JSON |
| `*sdk.CallToolResult` | `content[]` | clients that read text |

Two patterns are in use:

- **Return `nil` for the result** (`handleHostInfo`) — the SDK serializes `Out`
  into both channels. Right when the struct is already readable.
- **Return a `CallToolResult` holding rendered text** (everything else) — the
  text goes to `content`, `Out` still goes to `structuredContent`. Right when a
  table beats raw JSON.

Rendering is deliberate, not decorative: `system_disk_usage` returns a `df`-style
table and `system_memory_stats` a `free`-style one, because that shape is dense
and already familiar. The shared table renderer lives in `internal/mcp/render.go`.
Different clients favour different channels — some display `content`, some drop
it entirely when `structuredContent` is present — which is exactly why neither
channel is allowed to carry a fact the other lacks.

### Annotations

`ToolAnnotations` are **hints for the client's UI, not enforcement.** Nothing in
the SDK or the server refuses a call because `ReadOnlyHint` was true. They exist
so a client can group tools, warn before a destructive one, or skip a
confirmation for a safe one. `DestructiveHint` and `OpenWorldHint` default to
`true`, which is why `ptr(false)` exists in `render.go` — a plain `bool` cannot
express "explicitly false" for a `*bool` field.

### Conditional registration

The two state-changing tools are registered only when the allowlist is
non-empty:

```go
if allowed := containers.ActionAllowlist(); len(allowed) > 0 {
    sdk.AddTool(s, &sdk.Tool{Name: "docker_container_exec", ...}, handleExec)
    sdk.AddTool(s, &sdk.Tool{Name: "docker_container_restart", ...}, handleRestart)
}
```

A default install therefore exposes **7 read-only tools**; setting
`HOMELAB_MCP_ALLOW_CONTAINER_NAMES` raises that to **9**. The allowlist is also
interpolated into each description, so the model is told which containers it may
name instead of discovering it by being refused.

| Tool | Reads | Needs allowlist | Asks for confirmation |
|---|---|---|---|
| `system_host_info` | host | – | – |
| `system_cpu_cores` | `/proc/stat` | – | – |
| `system_memory_stats` | meminfo | – | – |
| `system_disk_usage` | statfs | – | – |
| `system_service_status` | `systemctl` | – | – |
| `docker_container_status` | docker socket | – | – |
| `docker_container_logs` | docker socket | – | – |
| `docker_container_exec` | docker socket | **yes** | **yes** |
| `docker_container_restart` | docker socket | **yes** | **yes** |

---

## 3. Waiting for a user response

### The problem

A tool call is one request and one response. A confirmation needs the server to
stop halfway, reach the human, and continue only if the answer is yes. Nothing
in a plain `tools/call` allows that.

### The mechanism

MCP's multi-round-trip flow (SEP-2322) gives the handler a way to answer "not
yet, ask this first". `internal/mcp/confirm.go` implements it in one place for
both action tools, and returns a three-state result:

```go
func requireApproval(req *sdk.CallToolRequest, a approval) (bool, *sdk.CallToolResult, error)
```

| Return | Meaning | What the handler does |
|---|---|---|
| `(false, pending, nil)` | ask the user | return `pending` unchanged |
| `(false, nil, err)` | do not proceed | return `err` |
| `(true, nil, nil)` | approved | perform the action |

**First pass** — the handler returns a result carrying no content, but an
input request and a `RequestState`:

```go
&sdk.CallToolResult{
    InputRequests: sdk.InputRequestMap{
        confirmKey: &sdk.ElicitParams{
            Message:         a.message,
            RequestedSchema: emptyElicitSchema(),
        },
    },
    RequestState: a.fingerprint,
}
```

**Second pass** — the client shows the message, collects the decision, and
**re-calls the same tool with the same arguments**, now carrying
`InputResponses` and echoing `RequestState`. `requireApproval` runs again, takes
the second branch, and validates the answer.

```mermaid
sequenceDiagram
    participant M as Model
    participant C as Client
    participant S as Server
    participant D as Docker

    M->>C: call docker_container_restart{container: jellyfin}
    C->>S: tools/call
    S->>S: allowlist check
    S-->>C: InputRequests{confirm}, RequestState=<fingerprint>
    C->>C: show the message to the human
    Note over C: accept / decline / cancel
    C->>S: tools/call (same args) + InputResponses + RequestState
    S->>S: action == accept ?
    S->>S: RequestState == fingerprint ?
    S->>D: POST /containers/{id}/restart
    D-->>S: 204
    S->>S: poll until settled (§4.5)
    S-->>C: result
```

### Two kinds of client

Whether the server can reach the human at all depends on a capability the client
declares at `initialize`:

- **Declares `elicitation`** — the server asks directly, per command, showing
  the exact command. This is the path above.
- **Does not declare it** (Claude Desktop, at the time of writing) — the server
  has no channel of its own. Those clients prompt for tool approval themselves,
  so a human is still in the loop, but the prompt is **per-tool, not
  per-command**, and a client set to always-allow stops asking.

For the second case the server refuses by default. Setting
`HOMELAB_MCP_TRUST_CLIENT_CONFIRMATION=1` makes it defer to the client's own
prompt instead. That variable is the operator vouching for their setup, and it
has to be the operator: **the identity a client reports at `initialize` is
self-declared and unauthenticated**, so a server cannot recognise a trustworthy
client — it can only be told. This is also why `clientName` feeds only the log
and the refusal message, and never a branch.

Which path a session got is logged once, at connect time:

```
client connected: name="claude-ai"; confirmations for actions: server-side, per command
client connected: name="claude-ai"; confirmations for actions: the client's own approval prompt
```

Without that line a refusal later looks arbitrary.

### `requestedSchema` is required

Form-mode elicitation requires `requestedSchema` in the MCP spec, even when the
server wants no fields back — the decision *is* the answer. We send an empty
object schema:

```go
map[string]any{"type": "object", "properties": map[string]any{}}
```

The Go SDK's own client tolerates its absence (`validateElicitSchema(nil)`
returns no error), so the round trip worked without it; a stricter client
following the published schema would not. Two consequences of sending it:

- Empty `properties` does **not** imply `additionalProperties: false`, so a
  client that returns content alongside its accept still validates.
- Sending a schema switches on result validation in the SDK, on both sides.

### Reading the answer

Anything short of an explicit accept stops the action:

| Answer | Outcome |
|---|---|
| `accept` | proceed to the fingerprint check |
| `decline` | `<refusal>: the user declined it` |
| `cancel` | `<refusal>: the user dismissed the confirmation without deciding` |
| anything else | `<refusal>: unrecognised confirmation action %q` |
| key missing | `<refusal>: no confirmation was returned` |
| wrong type | `<refusal>: confirmation response was not understood` |

The `refusal` prefix ("command not run", "container not restarted") is phrased so
that whatever the model reports back names **what did not happen**. A model that
paraphrases an error loosely still cannot turn it into "done".

---

## 4. Security

Three layers stand between the model and the container. They are independent and
fail in different directions on purpose.

### 4.1 Allowlist — what is reachable at all

`HOMELAB_MCP_ALLOW_CONTAINER_NAMES` is a comma-separated list of container names
(the `NAMES` column of `docker ps` — not the image, not the id). Empty means the
action tools are never registered, and the model cannot call a tool that does
not exist.

This is the only layer that **cannot be bypassed by a confused client, a client
that auto-approves everything, or a model under prompt injection.** The other
two depend on something outside the server behaving; this one does not.

One list covers every action, deliberately. A shell inside a container already
carries the power to take that container down, so "may run commands in X" and
"may restart X" are not separable grants — splitting them would advertise a
distinction the runtime does not enforce.

### 4.2 Human confirmation — §3

### 4.3 Fingerprint — binding the approval to the operation

The gap the first two layers leave: the confirmation and the execution are
**two separate tool calls**. The second one carries its own arguments. Without a
binding, a user could approve `ls /config` and the retry could arrive as
`rm -rf /config` — same tool, same session, different arguments, and the server
would have no way to notice.

`RequestState` closes that. The server puts a fingerprint of the exact operation
into the first response, the client echoes it back on the retry, and the server
recomputes it from the arguments it just received:

```go
if req.Params.RequestState != a.fingerprint {
    return false, nil, fmt.Errorf(
        "%s: the approved operation does not match the one submitted", a.refusal)
}
```

The hash itself (`internal/containers/exec.go`):

```go
func Fingerprint(container string, command []string) string {
	h := sha256.New()
	h.Write([]byte(container))
	for _, arg := range command {
		binary.Write(h, binary.BigEndian, uint32(len(arg)))
		h.Write([]byte(arg))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
```

**Why the length prefixes.** Concatenating arguments would make
`["rm", "-rf"]` and `["rm-rf"]` hash identically — the argv boundary is
semantic, and a hash that ignores it lets an approval for one command authorise
a different one. Writing each argument's length before its bytes makes the
encoding unambiguous. Verified:

| Property | Result |
|---|---|
| `["rm","-rf"]` vs `["rm-rf"]` | different |
| `("ab",["x"])` vs `("a",["bx"])` | different |
| output width | 32 hex chars = 128 bits |

**Why 128 bits.** The property that matters here is second-preimage resistance —
finding a *different* operation with the same fingerprint. 128 bits is far
beyond reach, and the value is echoed through a client, so a shorter string
keeps the protocol traffic readable.

**What the fingerprint proves:** the operation now being executed is
byte-for-byte the `(container, argv)` pair that was shown to the human.

**What it does not prove:**

- **Not freshness.** There is no nonce and no server-side record of issued
  fingerprints. It proves *same operation*, not *fresh approval*. A client that
  replayed a stored `RequestState` for an identical operation would pass. In
  practice `RequestState` is protocol-level and constructed by the client, not
  by the model — but the server does not enforce that.
- **Not the tool identity.** The fingerprint covers `(container, argv)` only.
  `docker_container_restart` uses the synthetic argv `["restart"]`, which
  **collides with an exec of the literal command `restart` in the same
  container** — verified: both produce `60139298fc75342a03fa46ad79c8117c` for
  `radarr`. Not currently reachable, because a well-behaved client builds
  `RequestState` itself and the model never chooses it. Closing it is one line —
  mix the tool name into the hash — and would remove the dependency on client
  behaviour.
- **Not the user's identity.** The server knows a decision came back through the
  session. It has no idea who made it.

### 4.4 Untrusted input that reaches a command line

Two places take a model-supplied string toward something that executes:

**systemd unit names** reach `systemctl`'s argv. Shell injection is already
impossible — `os/exec` calls `execve`, so there is no shell and metacharacters
are inert — but `systemctl` parses its own arguments, and a name starting with
`-` would be read as an option. `--host=` in particular makes it dial out over
SSH. So names are validated before anything runs:

```go
var validUnitName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@:\\-]{0,255}$`)
```

**Container names** never reach a request path at all. Every Docker call
resolves the name against the daemon's own listing first and then interpolates
the **id Docker returned**, so a caller-supplied string is only ever compared,
never used to build a URL.

### 4.5 Bounds

| Bound | Value | Why |
|---|---|---|
| `systemctl` timeout | 5s | systemd wedged would otherwise block the call |
| Docker API timeout | 5s | same, for an unresponsive daemon |
| statfs timeout | 2s | a dead NFS mount hangs statfs for minutes and ignores `ctx` |
| exec timeout | 30s default, 120s max | |
| exec output | 16 KiB, truncation reported | |
| log output | 16 KiB, truncation reported | |
| restart settle | 15s, 3s stable window | Docker reports "running" the instant the process spawns; a single check would pass a container that dies a second later |

Truncation is always reported in the result, never silent. Silent truncation
reads as a complete answer.

---

## 5. When something looks wrong

| Symptom | Where to look |
|---|---|
| Model says it has no action tools | Allowlist unset in the environment the server actually got — for an SSH-launched server, the `env` block in the client config reaches only the local `ssh` process, not the remote one |
| `Failed to call tool` on exec/restart | Client declares no elicitation and `HOMELAB_MCP_TRUST_CLIENT_CONFIRMATION` is unset — see the connect-time log line |
| Approved but refused | Fingerprint mismatch: the retry carried different arguments |
| A warning is missing from one client | It was built in the renderer instead of the collector (§1) |

---

## 6. Where to change what

| Change | File |
|---|---|
| Add a tool | `internal/mcp/register.go` + a `tool_*.go` |
| Change what the user reads before approving | the `approval.message` in the tool's handler |
| Change the confirmation flow itself | `internal/mcp/confirm.go` — one place, on purpose |
| Change what may be acted on | `internal/containers/allowlist.go` |
| Change table formatting | `internal/mcp/render.go` |
