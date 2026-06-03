.PHONY: build server agent run vet tidy test clean

build: server agent

server:
	go build -buildvcs=false -o bin/rmm-server ./cmd/server

agent:
	go build -buildvcs=false -o bin/rmm-agent ./cmd/agent

# Cross-compile the agent for the major endpoint platforms.
agent-all:
	GOOS=windows GOARCH=amd64 go build -buildvcs=false -o bin/rmm-agent-windows-amd64.exe ./cmd/agent
	GOOS=linux   GOARCH=amd64 go build -buildvcs=false -o bin/rmm-agent-linux-amd64       ./cmd/agent
	GOOS=darwin  GOARCH=arm64 go build -buildvcs=false -o bin/rmm-agent-darwin-arm64      ./cmd/agent

# Cross-compile the native remote media sidecar (cmd/rmm-remote). Windows has a
# working GDI/SendInput engine; other OSes build a stub for now.
remote-all:
	GOOS=windows GOARCH=amd64 go build -buildvcs=false -o bin/rmm-remote-windows-amd64.exe ./cmd/rmm-remote
	GOOS=linux   GOARCH=amd64 go build -buildvcs=false -o bin/rmm-remote-linux-amd64        ./cmd/rmm-remote
	GOOS=darwin  GOARCH=arm64 go build -buildvcs=false -o bin/rmm-remote-darwin-arm64       ./cmd/rmm-remote

run: server
	./bin/rmm-server

vet:
	go vet -buildvcs=false ./...

tidy:
	go mod tidy

test:
	go test -buildvcs=false ./...

clean:
	rm -rf bin
