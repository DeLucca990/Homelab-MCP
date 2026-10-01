INSPECTOR := @modelcontextprotocol/inspector@latest

# The service families' writes, switched off for `make dev` unless WRITE=1.
READONLY_ENV := HOMELAB_MCP_RADARR_READONLY=1 HOMELAB_MCP_SONARR_READONLY=1 \
	HOMELAB_MCP_PROWLARR_READONLY=1 HOMELAB_MCP_BAZARR_READONLY=1 \
	HOMELAB_MCP_JELLYFIN_READONLY=1

.PHONY: build deploy dev inspect inspect-open

build:
	go build -o bin/server ./cmd/server

deploy: build
	sudo systemctl restart homelab-mcp

dev: build
	HOMELAB_MCP_ALLOW_CONTAINER_NAMES= $(if $(WRITE),,$(READONLY_ENV)) ./bin/server

inspect:
	MCP_AUTO_OPEN_ENABLED=false npx $(INSPECTOR)

inspect-open:
	npx $(INSPECTOR)
