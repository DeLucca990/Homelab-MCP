package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/containers"
)

// DOCKER RESTART TOOL
//
// Same three layers as the exec tool: allowlist, per-call human confirmation,
// and a fingerprint tying the approval to what runs.

type restartInput struct {
	Container          string `json:"container" jsonschema:"name of the container to restart; must be in the server's restart allowlist"`
	StopTimeoutSeconds int    `json:"stop_timeout_seconds,omitempty" jsonschema:"how long to let the container shut down gracefully before it is killed; default 10, maximum 120"`
}

func handleRestart(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in restartInput,
) (*sdk.CallToolResult, containers.RestartResult, error) {
	if in.Container == "" {
		return nil, containers.RestartResult{}, fmt.Errorf("'container' is required")
	}

	approved, pending, err := requireApproval(req, approval{
		message: fmt.Sprintf(
			"Restart the container %q?\n\n"+
				"It will be stopped and started again. Anything it serves goes "+
				"offline for the duration, and unsaved in-flight work is lost.",
			in.Container),
		fingerprint: containers.Fingerprint(in.Container, []string{"restart"}),
		refusal:     "container not restarted",
		subject:     fmt.Sprintf("restart %s", in.Container),
	})
	if !approved {
		return pending, containers.RestartResult{}, err
	}

	out, err := containers.Restart(ctx, in.Container, in.StopTimeoutSeconds)
	if err != nil {
		return nil, containers.RestartResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderRestartResult(out)},
		},
	}, out, nil
}

func renderRestartResult(r containers.RestartResult) string {
	var b strings.Builder

	verdict := "did NOT come back"
	if r.CameBack {
		verdict = "is running again"
	}
	fmt.Fprintf(&b, "%s: %s -> %s (%s) in %dms\n",
		r.Container, r.PreviousState, r.State, verdict, r.DurationMS)

	if r.Health != "" {
		fmt.Fprintf(&b, "healthcheck: %s\n", r.Health)
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
