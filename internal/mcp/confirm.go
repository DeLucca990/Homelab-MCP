package mcp

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The human-in-the-loop round trip shared by every state-changing tool. It is
// the security boundary of the whole server, so it lives in exactly one place:
// a fix applied to one copy and missed in another would fail silently.

const confirmKey = "confirm"

// trustClientEnv is the operator vouching that their client prompts before
// calling a tool, so this server may act without a confirmation of its own.
// It has to be the operator: the identity a client reports at initialize is
// self-declared, so a server cannot recognise a trustworthy client, only be
// told. Unset, a client the server cannot question gets refused.
const trustClientEnv = "HOMELAB_MCP_TRUST_CLIENT_CONFIRMATION"

func trustClientConfirmation() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(trustClientEnv))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// A schema with no properties: the confirmation asks for a decision, not for
// input. Built per call rather than shared, so no request can mutate it.
func emptyElicitSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

// These read the session rather than the request because that is where the SDK
// keeps the identity a client declared. Over stateless HTTP there is no session
// to persist it, so the SDK rebuilds an ephemeral one per call out of the
// request's _meta — which a 2026-07-28 client sends and an older one does not.

// clientCanConfirm reports whether the client declared a channel the server can
// use to reach the user. Without it, an input request cannot be fulfilled.
func clientCanConfirm(ss *sdk.ServerSession) bool {
	params := initParams(ss)
	return params != nil && params.Capabilities != nil && params.Capabilities.Elicitation != nil
}

// clientName is for the log and the refusal message only. Nothing branches on
// it: it is self-declared, so it could not carry a decision honestly.
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

// fingerprint identifies one operation across the confirmation round trip. The
// first part is the tool name, so an approval given for one tool cannot satisfy
// another; the rest are whatever decides what the operation does.
func fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		binary.Write(h, binary.BigEndian, uint32(len(p)))
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// approval describes one thing the user is being asked to approve.
type approval struct {
	// What the user reads. States the action verbatim rather than summarising
	// it: this is the last point at which a human can tell the two apart.
	message string

	// Identifies the exact operation. Carried across the round trip and
	// re-checked, so an approval cannot be reused for different arguments.
	fingerprint string

	// Prefixes the error when the action does not happen, e.g. "command not
	// run". Phrased so the model reports what did NOT occur.
	refusal string

	// Names the operation in the server's log, e.g. "restart radarr" — the only
	// record of what this server did once the conversation is gone.
	subject string
}

// requireApproval drives the confirmation round trip. It returns
// (false, pending, nil) on the first pass, where pending is the result the
// handler must return to ask the user; (false, nil, err) when the action must
// not proceed; and (true, nil, nil) once the user has approved this exact
// operation.
func requireApproval(req *sdk.CallToolRequest, a approval) (bool, *sdk.CallToolResult, error) {
	// A client that declares no elicitation capability (Claude Desktop, at the
	// time of writing) leaves the server no channel of its own to reach the
	// user, so we defer to the approval prompt it shows before calling a tool.
	// The trade: that prompt is per-tool where ours is per-command, and a
	// client set to always-allow stops asking. The allowlist holds either way.
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

	// First pass: describe what would happen and hand the decision over.
	if len(req.Params.InputResponses) == 0 {
		return false, &sdk.CallToolResult{
			InputRequests: sdk.InputRequestMap{
				confirmKey: &sdk.ElicitParams{
					Message:         a.message,
					RequestedSchema: emptyElicitSchema(),
				},
			},
			RequestState: a.fingerprint,
		}, nil
	}

	// Second pass: anything short of an explicit accept stops here.
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

	// The retry carries its own parameters, which must still be the ones shown.
	if req.Params.RequestState != a.fingerprint {
		return false, nil, fmt.Errorf(
			"%s: the approved operation does not match the one submitted", a.refusal)
	}

	log.Printf("%s: approved by the user", a.subject)

	return true, nil, nil
}
