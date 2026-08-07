package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpserver "github.com/DeLucca990/homelab-mcp/internal/mcp"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[homelab-mcp] ")

	// Cancelled on Ctrl+C or SIGTERM (systemd).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := mcpserver.New()

	log.Println("MCP server running on transport stdio")

	if err := server.Run(ctx, &sdk.StdioTransport{}); err != nil {
		log.Fatalf("server stopped with error: %v", err)
	}

	log.Println("server stopped")
}
