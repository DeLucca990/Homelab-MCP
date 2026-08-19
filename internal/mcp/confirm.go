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

// The human-in-the-loop round trip shared by every state-changing tool.
const confirmKey = "confirm"

// trustClientEnv is the operator vouching that their client prompts before
// calling a tool, so this server may act without a confirmation of its own.
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

// clientCanConfirm reports whether the client declared a channel the server can
// use to reach the user. Without it, an input request cannot be fulfilled.
func clientCanConfirm(ss *sdk.ServerSession) bool {
	params := initParams(ss)
	return params != nil && params.Capabilities != nil && params.Capabilities.Elicitation != nil
}

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
	message     string
	fingerprint string
	refusal     string
	subject     string
}

// requireApproval drives the confirmation round trip. It returns
// (false, pending, nil) on the first pass, where pending is the result the
// handler must return to ask the user; (false, nil, err) when the action must
// not proceed; and (true, nil, nil) once the user has approved this exact
// operation.
func requireApproval(req *sdk.CallToolRequest, a approval) (bool, *sdk.CallToolResult, error) {
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
