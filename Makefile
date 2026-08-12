INSPECTOR := @modelcontextprotocol/inspector@latest

.PHONY: build inspect inspect-open

build:
	go build -o bin/server ./cmd/server

deploy: build
	sudo systemctl restart homelab-mcp

inspect:
	MCP_AUTO_OPEN_ENABLED=false npx $(INSPECTOR)

inspect-open:
	npx $(INSPECTOR)
