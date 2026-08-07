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
// Same three layers as the exec tool — allowlist, per-call human confirmation,
// and a fingerprint tying the approval to what runs — but on its own allowlist,
// because restarting a service is a different grant from debugging inside it.
const restartConfirmKey = "confirm"

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

	// The fingerprint covers the container name; the stop timeout cannot turn a
	// restart into a different operation, so it is not part of the identity.
	fingerprint := containers.Fingerprint(in.Container, []string{"restart"})

	if len(req.Params.InputResponses) == 0 {
		return &sdk.CallToolResult{
			InputRequests: sdk.InputRequestMap{
				restartConfirmKey: &sdk.ElicitParams{
					Message: fmt.Sprintf(
						"Restart the container %q?\n\n"+
							"It will be stopped and started again. Anything it serves goes "+
							"offline for the duration, and unsaved in-flight work is lost.",
						in.Container),
				},
			},
			RequestState: fingerprint,
		}, containers.RestartResult{}, nil
	}

	resp, ok := req.Params.InputResponses[restartConfirmKey]
	if !ok {
		return nil, containers.RestartResult{},
			fmt.Errorf("container not restarted: no confirmation was returned")
	}
	result, ok := resp.(*sdk.ElicitResult)
	if !ok || result == nil {
		return nil, containers.RestartResult{},
			fmt.Errorf("container not restarted: confirmation response was not understood")
	}
	switch result.Action {
	case "accept":
		// proceed
	case "decline":
		return nil, containers.RestartResult{},
			fmt.Errorf("container not restarted: the user declined it")
	case "cancel":
		return nil, containers.RestartResult{},
			fmt.Errorf("container not restarted: the user dismissed the confirmation without deciding")
	default:
		return nil, containers.RestartResult{},
			fmt.Errorf("container not restarted: unrecognised confirmation action %q", result.Action)
	}

	if req.Params.RequestState != fingerprint {
		return nil, containers.RestartResult{}, fmt.Errorf(
			"container not restarted: the approved container does not match the one submitted")
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
