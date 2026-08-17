package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/DeLucca990/homelab-mcp/internal/dotenv"
	mcpserver "github.com/DeLucca990/homelab-mcp/internal/mcp"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[homelab-mcp] ")

	dotenv.LoadEnvVariables()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := mcpserver.New()

	if err := mcpserver.ServeHTTP(ctx, server, mcpserver.HTTPAddr()); err != nil {
		log.Fatalf("server stopped with error: %v", err)
	}

	log.Println("server stopped")
}
