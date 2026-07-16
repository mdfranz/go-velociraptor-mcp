MODULE := github.com/mdfranz/go-velociraptor-mcp
CLI_BIN := raptor-cli
MCP_BIN := raptor-mcp
INSTALL_DIR ?= $(HOME)/.local/bin

.PHONY: all build cli mcp clean test fmt vet lint install

all: build

build: cli mcp

cli:
	go build -o $(CLI_BIN) ./cmd/raptor-cli

mcp:
	go build -o $(MCP_BIN) ./cmd/raptor-mcp

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

clean:
	rm -f $(CLI_BIN) $(MCP_BIN)

install: build
	install -d $(INSTALL_DIR)
	install -m 755 $(CLI_BIN) $(MCP_BIN) $(INSTALL_DIR)

tidy:
	go mod tidy
