package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	_ "time/tzdata"

	"github.com/DeLucca990/homelab-mcp/internal/dotenv"
	mcpserver "github.com/DeLucca990/homelab-mcp/internal/mcp"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[homelab-mcp] ")

	dotenv.LoadEnvVariables()

	switch command := strings.Join(os.Args[1:], " "); command {
	case "":
		serve()
	case "healthcheck":
		if err := mcpserver.Healthy(); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown command %q: server serves MCP; server healthcheck says whether "+
			"a running server is answering", command)
	}
}

func serve() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := mcpserver.New()

	if err := mcpserver.ServeHTTP(ctx, server, mcpserver.HTTPAddr()); err != nil {
		log.Fatalf("server stopped with error: %v", err)
	}

	log.Println("server stopped")
}
