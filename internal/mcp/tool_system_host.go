package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// HOST TOOL
//
// Every handler has this shape: func(ctx, *sdk.CallToolRequest, In)
// (*sdk.CallToolResult, Out, error).
func handleHostInfo(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, system.HostInfo, error) {
	info, err := system.GetHostInfo(ctx)
	if err != nil {
		return nil, system.HostInfo{}, err
	}
	// A nil CallToolResult lets the SDK serialize info as both content and
	// structuredContent.
	return nil, info, nil
}
