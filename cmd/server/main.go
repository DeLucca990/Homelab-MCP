package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/dotenv"
	mcpserver "github.com/DeLucca990/homelab-mcp/internal/mcp"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[homelab-mcp] ")

	loadEnvFile()

	// Cancelled on Ctrl+C or SIGTERM (systemd).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := mcpserver.New()

	if err := run(ctx, server); err != nil {
		log.Fatalf("server stopped with error: %v", err)
	}

	log.Println("server stopped")
}

func run(ctx context.Context, server *sdk.Server) error {
	if addr := mcpserver.HTTPAddr(); addr != "" {
		return mcpserver.ServeHTTP(ctx, server, addr)
	}

	log.Println("MCP server running on transport stdio")
	return server.Run(ctx, &sdk.StdioTransport{})
}

func loadEnvFile() {
	loaded, err := dotenv.Load()
	if err != nil {
		log.Printf("env file: %v — continuing without it", err)
		return
	}
	if loaded.Path == "" {
		return
	}

	log.Printf("env file %s: set %s", loaded.Path, names(loaded.Applied))
	if len(loaded.Skipped) > 0 {
		log.Printf("env file %s: %s already set in the environment, left alone",
			loaded.Path, names(loaded.Skipped))
	}
	if dotenv.WorldReadable(loaded.Path) {
		log.Printf("env file %s is readable by every user on this host; "+
			"it holds an API key — chmod 600 it", loaded.Path)
	}
}

func names(vars []string) string {
	if len(vars) == 0 {
		return "nothing"
	}
	return strings.Join(vars, ", ")
}
