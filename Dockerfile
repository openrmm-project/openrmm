# Build the server binary.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -o /out/rmm-server ./cmd/server

# Minimal runtime image.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/rmm-server /usr/local/bin/rmm-server
EXPOSE 8080
ENTRYPOINT ["rmm-server"]
