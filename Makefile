.PHONY: build server agent signer run vet tidy test security-check clean dist-clean checksums require-release-config agent-all remote-all remote-all-darwin remote-dist remote-dist-darwin agent-dist sign-dist agent-dist-signed remote-dist-signed dist-signed

# Go toolchain. Override if `go` isn't on PATH, e.g.:
#   make remote-dist GO=$(ls -d $HOME/go/pkg/mod/golang.org/toolchain@*/bin/go | head -1)
GO ?= go
VERSION ?= 0.5.0
AGENT_LDFLAGS := -X main.agentVersion=$(patsubst v%,%,$(VERSION))
ifdef RMM_UPDATE_PUBLIC_KEY
AGENT_LDFLAGS += -X main.updatePublicKey=$(RMM_UPDATE_PUBLIC_KEY)
endif
AGENT_BUILD_FLAGS := -buildvcs=false -ldflags '$(AGENT_LDFLAGS)'

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
	GOOS=windows GOARCH=arm64 $(GO) build $(AGENT_BUILD_FLAGS) -o bin/rmm-agent-windows-arm64.exe ./cmd/agent
	GOOS=linux   GOARCH=amd64 $(GO) build $(AGENT_BUILD_FLAGS) -o bin/rmm-agent-linux-amd64       ./cmd/agent
	GOOS=linux   GOARCH=arm64 $(GO) build $(AGENT_BUILD_FLAGS) -o bin/rmm-agent-linux-arm64       ./cmd/agent
	GOOS=darwin  GOARCH=amd64 $(GO) build $(AGENT_BUILD_FLAGS) -o bin/rmm-agent-darwin-amd64      ./cmd/agent
	GOOS=darwin  GOARCH=arm64 $(GO) build $(AGENT_BUILD_FLAGS) -o bin/rmm-agent-darwin-arm64      ./cmd/agent

# Cross-compile the native remote media sidecar (cmd/rmm-remote). Windows
# (GDI/SendInput) and Linux (X11/XTEST) are CGO-free and cross-compile from any
# host. macOS (CoreGraphics/CGEvent, media_darwin.go) needs CGO and Apple's
# frameworks, so it builds via the *-darwin targets below — run those ON A MAC
# (or with an osxcross toolchain). All share the tile-delta codec (encoder.go).
remote-all:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o bin/rmm-remote-windows-amd64.exe ./cmd/rmm-remote
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o bin/rmm-remote-windows-arm64.exe ./cmd/rmm-remote
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o bin/rmm-remote-linux-amd64        ./cmd/rmm-remote
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o bin/rmm-remote-linux-arm64        ./cmd/rmm-remote

# Publish the sidecar binaries to deploy/public/ so the server serves them at
# /dl/ for the agent's ETag-based auto-update (gitignored; rebuild before deploy).
remote-dist:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o deploy/public/rmm-remote-windows-amd64.exe ./cmd/rmm-remote
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o deploy/public/rmm-remote-windows-arm64.exe ./cmd/rmm-remote
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o deploy/public/rmm-remote-linux-amd64        ./cmd/rmm-remote
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 $(GO) build -buildvcs=false -o deploy/public/rmm-remote-linux-arm64        ./cmd/rmm-remote

# macOS sidecar — CGO (CoreGraphics + CGEvent). RUN ON A MAC: CGO_ENABLED=1 with
# Apple's frameworks; `go` cross-compiles arch via clang's -arch. On Apple
# Silicon the arm64 build is native; amd64 needs the macOS SDK (default present).
remote-all-darwin:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 $(GO) build -buildvcs=false -o bin/rmm-remote-darwin-arm64 ./cmd/rmm-remote
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 $(GO) build -buildvcs=false -o bin/rmm-remote-darwin-amd64 ./cmd/rmm-remote

# The embedded Info.plist helps identify the legacy bare binary; the .app bundle
# is what macOS attributes privacy grants to. Ad-hoc signatures are build-bound.
DARWIN_PLIST_LDFLAGS := -extldflags -Wl,-sectcreate,__TEXT,__info_plist,cmd/rmm-remote/macapp/Info.plist

# Produces both shapes: the .app.tar.gz the agent installs (privacy grants are
# keyed to a bundle) and the bare binary, which older agents still ask for.
remote-dist-darwin:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 $(GO) build -buildvcs=false -ldflags '$(DARWIN_PLIST_LDFLAGS)' -o deploy/public/rmm-remote-darwin-arm64 ./cmd/rmm-remote
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 CC="clang -arch x86_64" $(GO) build -buildvcs=false -ldflags '$(DARWIN_PLIST_LDFLAGS)' -o deploy/public/rmm-remote-darwin-amd64 ./cmd/rmm-remote
	@set -e; for arch in arm64 amd64; do \
	  ./scripts/build-macos-bundle.sh "$$arch" "deploy/public/rmm-remote-darwin-$$arch" "deploy/public/rmm-remote-darwin-$$arch.app.tar.gz"; \
	  codesign --sign "$${RMM_MACOS_CODESIGN_IDENTITY:--}" --force --identifier io.openrmm.remote "deploy/public/rmm-remote-darwin-$$arch"; \
	done

dist-clean:
	rm -f deploy/public/rmm-agent-* deploy/public/rmm-remote-* deploy/public/SHA256SUMS deploy/public/SHA256SUMS.new deploy/public/SHA256SUMS.sig deploy/public/SBOM.* deploy/public/*.intoto.jsonl

checksums:
	@set -e; tmp=deploy/public/SHA256SUMS.new; : > "$$tmp"; for file in deploy/public/rmm-agent-* deploy/public/rmm-remote-*; do case "$$file" in *.sig) continue;; esac; if test -f "$$file"; then hash=$$(openssl dgst -sha256 "$$file" | cut -d ' ' -f 2); printf '%s  %s\n' "$$hash" "$${file##*/}" >> "$$tmp"; fi; done; test -s "$$tmp"; mv "$$tmp" deploy/public/SHA256SUMS

# Builds every CGO-free target. On macOS it also includes both native
# CoreGraphics sidecars; other hosts leave those to the native release job.
dist-signed:
	@$(MAKE) require-release-config
	@$(MAKE) dist-clean
	@$(MAKE) agent-dist remote-dist
	@if test "$$(uname -s)" = Darwin; then $(MAKE) remote-dist-darwin; fi
	@$(MAKE) checksums
	@$(MAKE) sign-dist

run: server
	./bin/rmm-server

vet:
	$(GO) vet -buildvcs=false ./...

tidy:
	$(GO) mod tidy

# Applies pending migrations with the database OWNER's credentials.
#
# The server cannot do this itself any more, and that is the point: it connects
# as a restricted role that cannot alter the schema, which is what stops a
# compromised server from deleting the audit trail. So the owner credential is
# used here, for a few seconds, in a container that never serves a request.
#
# Only needed when a new version brings a migration. Forgetting is not silent —
# the server refuses to start and prints what to run.
#
# The DSN is not hardcoded on purpose. It differs per installation, and baking a
# development password into a tracked file would work here and fail for everyone
# who set their own, with an authentication error that reads like a broken
# Makefile rather than missing configuration.
migrate:
	@test -n "$$RMM_MIGRATE_DATABASE_URL" || { \
	  echo "RMM_MIGRATE_DATABASE_URL is not set."; \
	  echo ""; \
	  echo "It must point at the database OWNER — the role the server does NOT use."; \
	  echo "Set it in .env; see .env.example for the development value."; \
	  exit 1; }
	@echo "Applying migrations to $${RMM_MIGRATE_DATABASE_URL%%\?*}"
	@docker compose run --rm --no-deps \
	  -e RMM_DATABASE_URL="$$RMM_MIGRATE_DATABASE_URL" \
	  server -migrate-only

test:
	$(GO) test -buildvcs=false ./...

security-check:
	$(GO) mod verify
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...

clean:
	rm -rf bin
