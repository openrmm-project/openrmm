ARG GO_IMAGE=golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83
ARG NODE_IMAGE=node:24.4.1-alpine@sha256:820e86612c21d0636580206d802a726f2595366e1b867e564cbc652024151e8a
ARG ALPINE_IMAGE=alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
ARG NGINX_IMAGE=nginx:1.29-alpine@sha256:5616878291a2eed594aee8db4dade5878cf7edcb475e59193904b198d9b830de

# Build the server binary. go.mod requires Go >= 1.25.
FROM ${GO_IMAGE} AS server-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/rmm-server ./cmd/server

# Build the production console into openrmm/dist without carrying node_modules
# or source files into the web image.
FROM ${NODE_IMAGE} AS frontend-build
ARG OPENRMM_BASE_PATH=/
WORKDIR /src/openrmm
COPY openrmm/package.json openrmm/package-lock.json ./
RUN npm ci
COPY openrmm/index.html openrmm/config.js openrmm/tailwind.config.cjs ./
COPY openrmm/scripts ./scripts
COPY openrmm/src ./src
COPY openrmm/assets ./assets
RUN OPENRMM_BASE_PATH="$OPENRMM_BASE_PATH" npm run build

FROM ${ALPINE_IMAGE} AS server
# apk upgrade pulls the latest patched OS packages so the image is not stuck on
# the (older) package set baked into the pinned base — keeps Trivy clean between
# upstream base-image rebuilds.
RUN apk upgrade --no-cache \
    && apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 rmm \
    && adduser -S -D -H -u 10001 -G rmm rmm
COPY --from=server-build --chown=10001:10001 /out/rmm-server /usr/local/bin/rmm-server
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD wget -q --spider http://127.0.0.1:8080/readyz || exit 1
ENTRYPOINT ["/usr/local/bin/rmm-server"]

# The signed update artifacts, as an image, so a Kubernetes install can serve
# /dl/ without shared storage: an initContainer copies them into a volume the
# web container reads. The files are what `make dist-signed` produced —
# binaries, their .sig files, the manifest and its signature — and the image tag
# IS the release identity, which makes promoting a tenant a single value change
# rather than a file sync nobody can audit afterwards.
#
# Nothing here needs to be trusted: every artifact carries an Ed25519 signature
# the agent verifies against the key pinned at install time, so this image is a
# transport, not a trust boundary.
FROM ${ALPINE_IMAGE} AS artifacts
RUN apk upgrade --no-cache
COPY deploy/public/ /artifacts/
# Ownership matches the nginx user in the frontend image, so the copy lands
# readable without the initContainer having to chown anything.
RUN chown -R 101:101 /artifacts && chmod -R a-w /artifacts
USER 101:101
CMD ["sh", "-c", "cp -a /artifacts/. ${RMM_ARTIFACT_DEST:-/dl}/"]

FROM ${NGINX_IMAGE} AS frontend
# Patch OS packages (openssl, expat, libpng, c-ares, curl, ...) beyond what the
# pinned nginx base ships; without this the image carries fixable HIGH/CRITICAL
# CVEs that fail the Trivy gate.
RUN apk upgrade --no-cache
COPY --from=frontend-build --chown=101:101 /src/openrmm/dist/ /usr/share/nginx/html/
ENV OPENRMM_BASE_PATH=/ \
    NGINX_ENVSUBST_FILTER=^OPENRMM_BASE_PATH$
USER 101:101
EXPOSE 8080 8081 8082
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD base="${OPENRMM_BASE_PATH:-/}"; wget -q --spider "http://127.0.0.1:8081/${base#/}index.html" && wget -q --spider "http://127.0.0.1:8081/${base#/}assets/app.js" || exit 1
CMD ["nginx", "-g", "daemon off;"]
