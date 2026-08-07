package mcp

import (
	"fmt"

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
}

// requireApproval drives the confirmation round trip.
//
// It returns (false, pending, nil) on the first pass, where pending is the
// result the handler must return to ask the user; (false, nil, err) when the
// action must not proceed; and (true, nil, nil) once the user has approved
// this exact operation.
func requireApproval(req *sdk.CallToolRequest, a approval) (bool, *sdk.CallToolResult, error) {
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

	return true, nil, nil
}
