package containers

import (
	"os"
	"strings"
)

// AllowlistEnv holds a comma-separated list of container names — the NAMES
// column of `docker ps`, not the image and not the id — that the
// state-changing tools may touch. Unset or empty registers no action tool at
// all, so a default install stays entirely read-only.
//
// One list covers every action: a shell inside a container already carries the
// power to take that container down, so "may run commands in X" and "may
// restart X" are not separable grants.
//
// This is the only layer a confused client, an auto-approving client or a model
// under prompt injection cannot get around.
const AllowlistEnv = "HOMELAB_MCP_ALLOW_CONTAINER_NAMES"

// The read-only tools need no allowlist and ignore this entirely.
func ActionAllowlist() []string { return parseAllowlist(os.Getenv(AllowlistEnv)) }

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
