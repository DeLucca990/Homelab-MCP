INSPECTOR := @modelcontextprotocol/inspector@latest

.PHONY: inspect build

build:
	go build -o bin/server ./cmd/server

# inspect: build
# 	npx $(INSPECTOR) ./bin/server

inspect-open:
	npx $(INSPECTOR) go run ./cmd/server

inspect:
	MCP_AUTO_OPEN_ENABLED=false npx $(INSPECTOR) go run ./cmd/server