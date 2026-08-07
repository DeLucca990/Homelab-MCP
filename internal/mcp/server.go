package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Creates MCP server with all tools registered
func New() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{
		Name:    "homelab-monitor",
		Version: "1.0.0",
	}, nil) // Second param are "options"; nil = default

	registerTools(server)

	return server
}
