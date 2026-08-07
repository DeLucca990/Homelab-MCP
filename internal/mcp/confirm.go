package mcp

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The human-in-the-loop round trip shared by every state-changing tool.
//
// This lives in one place deliberately. It is the security boundary of the
// whole server, and the failure mode of duplicating it is silent: a fix applied
// to one copy and missed in the other leaves a tool that skips the fingerprint
// check or mishandles a dismissal, and nothing about the code looks wrong.

// confirmKey is the id under which the approval travels. Servers assign these
// themselves and there is only ever one outstanding
const confirmKey = "confirm"

// trustClientEnv states that the client connecting to this server prompts the
// user itself, so the server may act without a confirmation of its own.
//
// This is the operator vouching for their setup, which is the only form the
// statement can honestly take: the identity a client reports at initialize is
// self-declared and unauthenticated, so a server cannot recognise a trustworthy
// client — it can only be told. With this unset, a client the server cannot
// question gets refused.
const trustClientEnv = "HOMELAB_MCP_TRUST_CLIENT_CONFIRMATION"

func trustClientConfirmation() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(trustClientEnv))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// These take the session rather than a request because the two callers hold
// different request types — a tool call and an initialize notification — over
// the same session. Keyed on the session, one implementation serves both.

// clientCanConfirm reports whether the client declared a channel the server can
// use to reach the user. Without it, an input request cannot be fulfilled.
func clientCanConfirm(ss *sdk.ServerSession) bool {
	params := initParams(ss)
	return params != nil && params.Capabilities != nil && params.Capabilities.Elicitation != nil
}

// clientName is what the client called itself at initialize, or "" if it said
// nothing. Nothing branches on it — it is self-declared, so it could not carry
// a decision honestly. It exists so the log and the refusal message can name
// who was on the other end, which is the first thing you need when either one
// surprises you.
func clientName(ss *sdk.ServerSession) string {
	if params := initParams(ss); params != nil && params.ClientInfo != nil {
		return params.ClientInfo.Name
	}
	return ""
}

func initParams(ss *sdk.ServerSession) *sdk.InitializeParams {
	if ss == nil {
		return nil
	}
	return ss.InitializeParams()
}

// logClientIdentity records who connected and which confirmation mechanism will
// apply for the session. Both are decided at connect time and are invisible
// everywhere else: a refusal further on otherwise looks arbitrary, and an
// action taken on the client's own approval leaves no trace of who approved it.
func logClientIdentity(_ context.Context, req *sdk.InitializedRequest) {
	confirmation := "the client's own approval prompt"
	if clientCanConfirm(req.Session) {
		confirmation = "server-side, per command"
	}

	log.Printf("client connected: name=%q; confirmations for actions: %s",
		clientName(req.Session), confirmation)
}

// approval describes one thing the user is being asked to approve.
type approval struct {
	// message is what the user reads. It should state the action verbatim
	// rather than summarising it — this is the last point at which a human can
	// tell the difference between what was asked for and what will happen.
	message string

	// fingerprint identifies the exact operation. It is carried across the
	// round trip and re-checked, so an approval cannot be reused for different
	// arguments.
	fingerprint string

	// refusal prefixes the error when the action does not happen, e.g.
	// "command not run". Phrased so the model reports what did NOT occur.
	refusal string

	// subject names the operation for the server's own log, e.g.
	// "restart radarr". Every state-changing call leaves a line, which is the
	// only record of what this server did once the conversation is gone.
	subject string
}

// requireApproval drives the confirmation round trip.
//
// It returns (false, pending, nil) on the first pass, where pending is the
// result the handler must return to ask the user; (false, nil, err) when the
// action must not proceed; and (true, nil, nil) once the user has approved
// this exact operation.
func requireApproval(req *sdk.CallToolRequest, a approval) (bool, *sdk.CallToolResult, error) {
	// Some clients — Claude Desktop among them at the time of writing — do not
	// declare the elicitation capability, so the server has no channel of its
	// own to reach the user. Those clients prompt for tool approval themselves,
	// so the human is still in the loop; the approval simply comes from the
	// client rather than from here.
	//
	// We defer to it rather than refusing. The trade is real and worth stating:
	// the client's prompt is per-tool where ours is per-command, and a client
	// configured to always-allow stops asking. What still holds either way is
	// the allowlist, which no client can widen.
	if len(req.Params.InputResponses) == 0 && !clientCanConfirm(req.Session) {
		if !trustClientConfirmation() {
			return false, nil, fmt.Errorf(
				"%s: this client (%q) cannot show a confirmation coming from the server, "+
					"and this server will not act without one. Either use a client that "+
					"supports MCP elicitation, or set %s=1 to accept the approval prompt the "+
					"client shows before calling a tool — that is per-tool rather than "+
					"per-command, and a client set to always-allow stops asking",
				a.refusal, clientName(req.Session), trustClientEnv)
		}

		log.Printf("%s: proceeding on the approval prompt of %q (%s is set)",
			a.subject, clientName(req.Session), trustClientEnv)
		return true, nil, nil
	}

	// FIRST PASS: nothing has been approved yet. Describe what would happen and
	// hand the decision to the user.
	if len(req.Params.InputResponses) == 0 {
		return false, &sdk.CallToolResult{
			InputRequests: sdk.InputRequestMap{
				confirmKey: &sdk.ElicitParams{Message: a.message},
			},
			RequestState: a.fingerprint,
		}, nil
	}

	// SECOND PASS: an answer came back. Anything short of an explicit accept
	// stops here.
	resp, ok := req.Params.InputResponses[confirmKey]
	if !ok {
		return false, nil, fmt.Errorf("%s: no confirmation was returned", a.refusal)
	}
	result, ok := resp.(*sdk.ElicitResult)
	if !ok || result == nil {
		return false, nil, fmt.Errorf("%s: confirmation response was not understood", a.refusal)
	}

	switch result.Action {
	case "accept":
		// proceed
	case "decline":
		return false, nil, fmt.Errorf("%s: the user declined it", a.refusal)
	case "cancel":
		return false, nil, fmt.Errorf("%s: the user dismissed the confirmation without deciding", a.refusal)
	default:
		return false, nil, fmt.Errorf("%s: unrecognised confirmation action %q", a.refusal, result.Action)
	}

	// The user approved a specific operation. Verify it is still the one being
	// asked for: the retry carries its own parameters, and they must match what
	// was shown.
	if req.Params.RequestState != a.fingerprint {
		return false, nil, fmt.Errorf(
			"%s: the approved operation does not match the one submitted", a.refusal)
	}

	log.Printf("%s: approved by the user", a.subject)

	return true, nil, nil
}
