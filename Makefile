INSPECTOR := @modelcontextprotocol/inspector@latest

# The service families' writes, switched off for `make dev` unless WRITE=1.
READONLY_ENV := NAUTILUS_RADARR_READONLY=1 NAUTILUS_SONARR_READONLY=1 \
	NAUTILUS_PROWLARR_READONLY=1 NAUTILUS_BAZARR_READONLY=1 \
	NAUTILUS_JELLYFIN_READONLY=1

.PHONY: build test deploy deploy-bot dev inspect inspect-open up down logs-mcp logs-bot

build:
	go build -o bin/server ./cmd/server
	go build -o bin/telegram-bot ./cmd/telegram-bot

test:
	go test ./...

dev: build
	NAUTILUS_ALLOW_CONTAINER_NAMES= $(if $(WRITE),,$(READONLY_ENV)) ./bin/server

inspect:
	MCP_AUTO_OPEN_ENABLED=false npx $(INSPECTOR)

inspect-open:
	npx $(INSPECTOR)

up:
	docker compose up -d --build

down:
	docker compose down

logs-mcp:
	docker compose logs -f nautilus-mcp

logs-bot:
	docker compose logs -f nautilus-bot
