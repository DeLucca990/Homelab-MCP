package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// CPU TOOL
type coreUsageInput struct {
	SampleMS int `json:"sample_ms,omitempty" jsonschema:"sampling window in milliseconds; default 500, maximum 5000"`
}

type coreUsageOutput struct {
	Cores          []system.CoreUsage `json:"cores"`
	SampleWindowMs int                `json:"sample_window_ms"`
}

func handleCoreUsage(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in coreUsageInput,
) (*sdk.CallToolResult, coreUsageOutput, error) {
	interval := time.Duration(in.SampleMS) * time.Millisecond

	cores, err := system.GetCoreUsage(ctx, interval)
	if err != nil {
		return nil, coreUsageOutput{}, err
	}

	out := coreUsageOutput{Cores: cores, SampleWindowMs: in.SampleMS}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderCores(cores)},
		},
	}, out, nil
}

// One dense line per core. Deliberately no ASCII bar: it would be a visual
// encoding of the number printed right beside it, costing tokens to say the
// same thing. The full breakdown stays in structuredContent.
func renderCores(cores []system.CoreUsage) string {
	var b strings.Builder
	for _, c := range cores {
		fmt.Fprintf(&b, "%-6s %5.1f%%  (usr %.1f  sys %.1f  io %.1f)\n",
			c.Core, c.TotalPercent,
			c.UserPercent, c.SystemPercent, c.IOWaitPercent)
	}
	return b.String()
}
