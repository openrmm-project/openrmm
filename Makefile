.PHONY: build server agent run vet tidy test clean

# Go toolchain. Override if `go` isn't on PATH, e.g.:
#   make remote-dist GO=$(ls -d $HOME/go/pkg/mod/golang.org/toolchain@*/bin/go | head -1)
GO ?= go

build: server agent

server:
	$(GO) build -buildvcs=false -o bin/rmm-server ./cmd/server

agent:
	$(GO) build -buildvcs=false -o bin/rmm-agent ./cmd/agent

# Cross-compile the agent for the major endpoint platforms.
agent-all:
	GOOS=windows GOARCH=amd64 $(GO) build -buildvcs=false -o bin/rmm-agent-windows-amd64.exe ./cmd/agent
	GOOS=linux   GOARCH=amd64 $(GO) build -buildvcs=false -o bin/rmm-agent-linux-amd64       ./cmd/agent
	GOOS=darwin  GOARCH=arm64 $(GO) build -buildvcs=false -o bin/rmm-agent-darwin-arm64      ./cmd/agent

# Cross-compile the native remote media sidecar (cmd/rmm-remote), CGO-free.
# Windows = GDI/SendInput; Linux = X11/XTEST (media_linux.go); macOS = stub
# (no CGO-free capture path yet). All share the tile-delta codec (encoder.go).
remote-all:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o bin/rmm-remote-windows-amd64.exe ./cmd/rmm-remote
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o bin/rmm-remote-linux-amd64        ./cmd/rmm-remote
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o bin/rmm-remote-darwin-arm64       ./cmd/rmm-remote

# Publish the sidecar binaries to deploy/public/ so the server serves them at
# /dl/ for the agent's ETag-based auto-update (gitignored; rebuild before deploy).
remote-dist:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o deploy/public/rmm-remote-windows-amd64.exe ./cmd/rmm-remote
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o deploy/public/rmm-remote-linux-amd64        ./cmd/rmm-remote
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o deploy/public/rmm-remote-darwin-arm64       ./cmd/rmm-remote

run: server
	./bin/rmm-server

vet:
	$(GO) vet -buildvcs=false ./...

tidy:
	$(GO) mod tidy

test:
	$(GO) test -buildvcs=false ./...

clean:
	rm -rf bin
