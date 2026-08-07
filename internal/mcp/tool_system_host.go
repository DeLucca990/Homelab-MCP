package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// HOST TOOL
// Handle signature are always "func(ctx, *sdk.CallToolRequest, In) (*sdk.CallToolResult, Out, error)"
func handleHostInfo(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, system.HostInfo, error) {
	info, err := system.GetHostInfo(ctx)
	if err != nil {
		return nil, system.HostInfo{}, err
	}
	// Return nul on CallToolResult, the SDK automatically serializes "info" as content + JSON text
	return nil, info, nil
}
