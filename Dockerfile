FROM golang:1.15 AS builder
WORKDIR /sshcontainers
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -ldflags="-s -w"

FROM gcr.io/distroless/static
COPY --from=builder /sshcontainers/sshcontainers /usr/bin
ENTRYPOINT ["/usr/bin/sshcontainers"]
