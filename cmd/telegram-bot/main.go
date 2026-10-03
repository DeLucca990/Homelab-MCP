package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/DeLucca990/homelab-mcp/internal/dotenv"
	"github.com/DeLucca990/homelab-mcp/internal/telegram"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[homelab-telegram] ")

	dotenv.LoadEnvVariables()

	cfg, err := telegram.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bot, err := telegram.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	bot.Run(ctx)
	log.Println("bot stopped")
}
