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
	SampleMS int `json:"sample_ms,omitempty" jsonschema:"janela de amostragem em milissegundos; padrão 500, máximo 5000"`
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
			&sdk.TextContent{Text: renderCoreBars(cores)},
		},
	}, out, nil
}

func renderCoreBars(cores []system.CoreUsage) string {
	var b strings.Builder
	for _, c := range cores {
		fmt.Fprintf(&b, "%-6s [%s] %5.1f%%  (usr %.1f  sys %.1f  io %.1f)\n",
			c.Core, bar(c.TotalPercent, 30), c.TotalPercent,
			c.UserPercent, c.SystemPercent, c.IOWaitPercent)
	}
	return b.String()
}

func bar(percent float64, width int) string {
	filled := int(percent/100*float64(width) + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("|", filled) + strings.Repeat(" ", width-filled)
}
