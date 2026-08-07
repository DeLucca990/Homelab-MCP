INSPECTOR := @modelcontextprotocol/inspector@latest

.PHONY: inspect build

build:
	go build -o bin/server ./cmd/server

# inspect: build
# 	npx $(INSPECTOR) ./bin/server

inspect:
	npx $(INSPECTOR) go run ./cmd/server