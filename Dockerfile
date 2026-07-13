# Build the server binary. (go.mod requires Go >= 1.25.)
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -o /out/rmm-server ./cmd/server

# Minimal runtime image.
FROM alpine:3.24
RUN apk add --no-cache ca-certificates
COPY --from=build /out/rmm-server /usr/local/bin/rmm-server
EXPOSE 8080
ENTRYPOINT ["rmm-server"]
