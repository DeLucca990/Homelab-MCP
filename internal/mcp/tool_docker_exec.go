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
// Three independent layers stand between the model and the container:
//
//  1. Allowlist — HOMELAB_MCP_ALLOW_CONTAINER_NAMES, which nothing the model or
//     the client does can widen. Empty means the tool is never registered.
//  2. Human confirmation — every execution is approved first, by the server
//     through the protocol where the client supports it and by the client's own
//     prompt where it does not. See confirm.go for the trade.
//  3. Fingerprint — the approved (container, command) pair is re-checked on the
//     retry, so what runs is provably what was shown.

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

	approved, pending, err := requireApproval(req, approval{
		message:     confirmationMessage(in),
		fingerprint: containers.Fingerprint(in.Container, in.Command),
		refusal:     "command not run",
		subject:     fmt.Sprintf("exec in %s: %s", in.Container, strings.Join(in.Command, " ")),
	})
	if !approved {
		return pending, containers.ExecResult{}, err
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
