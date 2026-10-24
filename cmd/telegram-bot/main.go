package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	_ "time/tzdata"

	"github.com/DeLucca990/nautilus/internal/dotenv"
	"github.com/DeLucca990/nautilus/internal/telegram"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[nautilus-bot] ")

	dotenv.LoadEnvVariables()

	switch command := strings.Join(os.Args[1:], " "); command {
	case "":
		runBot()
	case "healthcheck":
		if err := telegram.Healthy(); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown command %q: telegram-bot runs the bot; telegram-bot healthcheck "+
			"says whether a running bot is answering", command)
	}
}

func runBot() {
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
