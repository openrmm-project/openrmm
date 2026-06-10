.PHONY: build server agent signer run vet tidy test security-check clean agent-all remote-all remote-dist agent-dist sign-dist agent-dist-signed remote-dist-signed dist-signed

# Go toolchain. Override if `go` isn't on PATH, e.g.:
#   make remote-dist GO=$(ls -d $HOME/go/pkg/mod/golang.org/toolchain@*/bin/go | head -1)
GO ?= go
AGENT_BUILD_FLAGS := -buildvcs=false
ifdef RMM_UPDATE_PUBLIC_KEY
AGENT_BUILD_FLAGS += -ldflags "-X main.updatePublicKey=$(RMM_UPDATE_PUBLIC_KEY)"
endif

build: server agent

server:
	$(GO) build -buildvcs=false -o bin/rmm-server ./cmd/server

agent:
	$(GO) build $(AGENT_BUILD_FLAGS) -o bin/rmm-agent ./cmd/agent

signer:
	$(GO) build -buildvcs=false -o bin/rmm-sign ./cmd/rmm-sign

# Cross-compile the agent for the major endpoint platforms.
agent-all:
	GOOS=windows GOARCH=amd64 $(GO) build $(AGENT_BUILD_FLAGS) -o bin/rmm-agent-windows-amd64.exe ./cmd/agent
	GOOS=linux   GOARCH=amd64 $(GO) build $(AGENT_BUILD_FLAGS) -o bin/rmm-agent-linux-amd64       ./cmd/agent
	GOOS=darwin  GOARCH=arm64 $(GO) build $(AGENT_BUILD_FLAGS) -o bin/rmm-agent-darwin-arm64      ./cmd/agent

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

# Publish the agent binaries to deploy/public/ so the server serves them at /dl/
# for the agent's ETag-based SELF-UPDATE (and the install scripts). Republish here
# and connected agents upgrade themselves within RMM_UPDATE_INTERVAL (default 30m).
# Names must match agentDownloadName(): rmm-agent-<os>-<arch>[.exe].
agent-dist:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(AGENT_BUILD_FLAGS) -o deploy/public/rmm-agent-windows-amd64.exe ./cmd/agent
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(AGENT_BUILD_FLAGS) -o deploy/public/rmm-agent-linux-amd64        ./cmd/agent
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 $(GO) build $(AGENT_BUILD_FLAGS) -o deploy/public/rmm-agent-linux-arm64        ./cmd/agent
	GOOS=darwin  GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(AGENT_BUILD_FLAGS) -o deploy/public/rmm-agent-darwin-amd64       ./cmd/agent
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 $(GO) build $(AGENT_BUILD_FLAGS) -o deploy/public/rmm-agent-darwin-arm64       ./cmd/agent

sign-dist: signer
	@test -n "$$RMM_SIGNING_KEY" || (echo "RMM_SIGNING_KEY is required (generate with: bin/rmm-sign -gen-key)" && exit 1)
	find deploy/public -maxdepth 1 -type f \( -name 'rmm-agent-*' -o -name 'rmm-remote-*' \) ! -name '*.sig' -exec ./bin/rmm-sign {} +

agent-dist-signed: agent-dist signer
	@test -n "$$RMM_SIGNING_KEY" || (echo "RMM_SIGNING_KEY is required (generate with: bin/rmm-sign -gen-key)" && exit 1)
	find deploy/public -maxdepth 1 -type f -name 'rmm-agent-*' ! -name '*.sig' -exec ./bin/rmm-sign {} +

remote-dist-signed: remote-dist signer
	@test -n "$$RMM_SIGNING_KEY" || (echo "RMM_SIGNING_KEY is required (generate with: bin/rmm-sign -gen-key)" && exit 1)
	find deploy/public -maxdepth 1 -type f -name 'rmm-remote-*' ! -name '*.sig' -exec ./bin/rmm-sign {} +

dist-signed: agent-dist remote-dist sign-dist

run: server
	./bin/rmm-server

vet:
	$(GO) vet -buildvcs=false ./...

tidy:
	$(GO) mod tidy

test:
	$(GO) test -buildvcs=false ./...

security-check:
	$(GO) mod verify
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...

clean:
	rm -rf bin
