# 02 — Getting Started

## Prerequisites

- Go 1.23+ (to build) or Docker (to run via compose).
- PostgreSQL 14+.

## Run with Docker Compose

```bash
cp .env.example .env
# edit .env: set a strong RMM_ADMIN_TOKEN and your AI keys
docker compose up -d
curl localhost:8080/healthz      # -> ok
```

## Run locally

```bash
# start a Postgres (example)
docker run -d --name rmm-pg -e POSTGRES_USER=rmm -e POSTGRES_PASSWORD=rmm \
  -e POSTGRES_DB=rmm -p 5432:5432 postgres:16-alpine

export RMM_ADMIN_TOKEN=$(openssl rand -hex 32)
export RMM_DATABASE_URL="postgres://rmm:rmm@localhost:5432/rmm?sslmode=disable"
export RMM_AI_API_KEY=sk-...        # or point RMM_AI_BASE_URL at Ollama/vLLM
make build
./bin/rmm-server
```

The schema is embedded and applied automatically on start (idempotent).

## Enroll an agent

```bash
# 1. Create an enrollment token (admin).
TOKEN=$(curl -s -X POST localhost:8080/api/enroll-tokens \
  -H "Authorization: Bearer $RMM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"description":"lab","uses":10}' | jq -r .token)

# 2. Run the agent (first run consumes the enroll token and stores its identity).
./bin/rmm-agent -server ws://localhost:8080/ws/agent -enroll-token "$TOKEN"

# 3. Confirm it registered.
curl -s localhost:8080/api/devices -H "Authorization: Bearer $RMM_ADMIN_TOKEN" | jq
```

Cross-compile the agent for endpoints with `make agent-all`.

## Run a job (with approval)

```bash
AID=<agent id from /api/devices>

# Propose (lands in awaiting_approval)
JID=$(curl -s -X POST localhost:8080/api/jobs \
  -H "Authorization: Bearer $RMM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d "{\"agent_id\":\"$AID\",\"shell\":\"bash\",\"script\":\"echo hi && uname -a\",\"description\":\"smoke\"}" \
  | jq -r .id)

# Approve -> dispatched -> executed
curl -s -X POST localhost:8080/api/jobs/$JID/approve -H "Authorization: Bearer $RMM_ADMIN_TOKEN" | jq

# Read the result
curl -s localhost:8080/api/jobs/$JID -H "Authorization: Bearer $RMM_ADMIN_TOKEN" | jq '{status, exit_code, stdout}'
```

## Use the AI chat

```bash
SID=$(curl -s -X POST localhost:8080/api/chat/sessions \
  -H "Authorization: Bearer $RMM_ADMIN_TOKEN" -d '{"title":"ops"}' | jq -r .id)

curl -s -X POST localhost:8080/api/chat/sessions/$SID/messages \
  -H "Authorization: Bearer $RMM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"content":"List my Linux devices and propose a script to install htop on the first one."}' | jq
```

The reply includes `created_job_ids` for any jobs the AI proposed — surface those
in the UI for approval. (Requires a working AI provider; set `RMM_AI_API_KEY` or
point `RMM_AI_BASE_URL` at a local model.)
