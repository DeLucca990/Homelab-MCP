package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Creates MCP server with all tools registered
func New() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{
		Name:    "homelab-monitor",
		Version: "1.0.0",
	}, &sdk.ServerOptions{
		InitializedHandler: logClientIdentity,
	})

	registerTools(server)

	return server
}
