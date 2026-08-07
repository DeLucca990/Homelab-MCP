package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// New builds the server with every tool registered.
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
