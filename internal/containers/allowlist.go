package containers

import (
	"os"
	"strings"
)

// AllowlistEnv names the environment variable that permits the state-changing
// tools to touch a container. It holds a comma-separated list of container
// names — the NAMES column of `docker ps`, not the image and not the id.
//
// One list covers every action. Splitting it per action would suggest a
// distinction the runtime does not enforce: a shell inside a container already
// carries the power to take that container down, so "may run commands in X"
// and "may restart X" are not meaningfully separable grants.
//
// Unset or empty means no action tool is registered at all: the model cannot
// call what does not exist, and a default install stays entirely read-only.
// This is the only layer that cannot be bypassed by a confused client, a client
// that auto-approves everything, or a model under prompt injection — so it is
// the one that decides what is reachable at all.
const AllowlistEnv = "HOMELAB_MCP_ALLOW_CONTAINER_NAMES"

// ActionAllowlist returns the container names the state-changing tools may act
// on. The read-only tools need no allowlist and ignore this entirely.
func ActionAllowlist() []string { return parseAllowlist(os.Getenv(AllowlistEnv)) }

// ActionsEnabled reports whether any container may be acted on at all.
func ActionsEnabled() bool { return len(ActionAllowlist()) > 0 }

func actionAllowed(name string) bool {
	for _, allowed := range ActionAllowlist() {
		if strings.EqualFold(allowed, name) {
			return true
		}
	}
	return false
}

func parseAllowlist(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		if name := strings.TrimPrefix(strings.TrimSpace(part), "/"); name != "" {
			out = append(out, name)
		}
	}
	return out
}
