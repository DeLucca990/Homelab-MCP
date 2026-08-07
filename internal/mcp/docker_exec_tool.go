package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/containers"
)

// DOCKER EXEC TOOL
//
// The only tool in this server that changes anything. Three independent layers
// stand between the model and the container, and they fail in different
// directions on purpose:
//
//  1. Allowlist — the server refuses any container not named in the
//     HOMELAB_MCP_EXEC_ALLOW_CONTAINER_NAMES environment variable, and the tool is not
//     registered at all when that variable is empty. Nothing the model or the
//     client does can widen this.
//  2. Human confirmation — every execution requires the user to approve THIS
//     command. Requested through the protocol (SEP-2322 input requests, which
//     the SDK degrades to elicitation on older clients), not left to the
//     client's own prompting, so "always allow" on the tool does not silently
//     turn it into a blank cheque.
//  3. Fingerprint — the approved (container, command) pair is hashed into the
//     request state and re-checked on the retry, so the command that runs is
//     provably the one that was shown.
//
// If the client cannot ask the user, the confirmation request fails and the
// command does not run. This fails closed by design.
const confirmKey = "confirm"

type execInput struct {
	Container      string   `json:"container" jsonschema:"name of the container to run the command in; must be in the server's exec allowlist"`
	Command        []string `json:"command" jsonschema:"the command as an argument vector, e.g. [\"ls\",\"-la\",\"/config\"]. This is NOT a shell line: it is executed directly, so pipes and redirection do nothing unless you explicitly request a shell with [\"sh\",\"-c\",\"...\"]"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty" jsonschema:"how long to wait before giving up; default 30, maximum 120"`
}

func handleExec(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in execInput,
) (*sdk.CallToolResult, containers.ExecResult, error) {
	if in.Container == "" || len(in.Command) == 0 {
		return nil, containers.ExecResult{},
			fmt.Errorf("both 'container' and a non-empty 'command' are required")
	}

	fingerprint := containers.Fingerprint(in.Container, in.Command)

	// FIRST PASS: nothing has been approved yet. Describe exactly what would
	// run and hand the decision to the user.
	if len(req.Params.InputResponses) == 0 {
		return &sdk.CallToolResult{
			InputRequests: sdk.InputRequestMap{
				confirmKey: &sdk.ElicitParams{Message: confirmationMessage(in)},
			},
			RequestState: fingerprint,
		}, containers.ExecResult{}, nil
	}

	// SECOND PASS: an answer came back. Anything other than an explicit accept
	// stops here.
	resp, ok := req.Params.InputResponses[confirmKey]
	if !ok {
		return nil, containers.ExecResult{},
			fmt.Errorf("command not run: no confirmation was returned")
	}
	result, ok := resp.(*sdk.ElicitResult)
	if !ok || result == nil {
		return nil, containers.ExecResult{},
			fmt.Errorf("command not run: confirmation response was not understood")
	}
	switch result.Action {
	case "accept":
		// proceed
	case "decline":
		return nil, containers.ExecResult{},
			fmt.Errorf("command not run: the user declined it")
	case "cancel":
		return nil, containers.ExecResult{},
			fmt.Errorf("command not run: the user dismissed the confirmation without deciding")
	default:
		return nil, containers.ExecResult{},
			fmt.Errorf("command not run: unrecognised confirmation action %q", result.Action)
	}

	// The user approved a specific command. Verify that is still the one being
	// asked for — the retry carries its own parameters, and they must match the
	// ones that were shown.
	if req.Params.RequestState != fingerprint {
		return nil, containers.ExecResult{}, fmt.Errorf(
			"command not run: the approved command does not match the one submitted")
	}

	out, err := containers.Exec(ctx, in.Container, in.Command,
		time.Duration(in.TimeoutSeconds)*time.Second)
	if err != nil {
		return nil, containers.ExecResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderExecResult(out)},
		},
	}, out, nil
}

// confirmationMessage is what the user actually reads before deciding, so it
// shows the command verbatim rather than summarising it.
func confirmationMessage(in execInput) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Run this command inside the container %q?\n\n", in.Container)
	fmt.Fprintf(&b, "    %s\n", strings.Join(in.Command, " "))

	if isShell(in.Command) {
		b.WriteString("\nThis runs a SHELL, so the text above is interpreted, " +
			"not executed literally.")
	}
	b.WriteString("\nIt runs as the container's default user and may change its state.")

	return b.String()
}

func isShell(command []string) bool {
	if len(command) == 0 {
		return false
	}
	switch command[0] {
	case "sh", "bash", "ash", "zsh", "/bin/sh", "/bin/bash", "/bin/ash", "/bin/zsh":
		return true
	}
	return false
}

func renderExecResult(r containers.ExecResult) string {
	var b strings.Builder

	fmt.Fprintf(&b, "$ %s\n", strings.Join(r.Command, " "))
	fmt.Fprintf(&b, "container: %s   exit: %d   took: %dms\n", r.Container, r.ExitCode, r.DurationMS)

	if r.TimedOut {
		b.WriteString("\nthe command was still running when the timeout expired; " +
			"output below is what it had produced by then\n")
	}

	if r.Stdout != "" {
		fmt.Fprintf(&b, "\n--- stdout ---\n%s", r.Stdout)
		if !strings.HasSuffix(r.Stdout, "\n") {
			b.WriteString("\n")
		}
	}
	if r.Stderr != "" {
		fmt.Fprintf(&b, "\n--- stderr ---\n%s", r.Stderr)
		if !strings.HasSuffix(r.Stderr, "\n") {
			b.WriteString("\n")
		}
	}
	if r.Stdout == "" && r.Stderr == "" {
		b.WriteString("\n(no output)\n")
	}

	if r.Truncated {
		b.WriteString("\nwarning: output was truncated at the size limit — " +
			"what is shown is the beginning, not the end\n")
	}

	return b.String()
}
