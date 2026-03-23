FROM golang:1.25.8 AS builder
WORKDIR /app
COPY . .
RUN go build -o fh-svc cmd/fh-svc/main.go
RUN go build -o fh-cli cmd/fh-cli/main.go

FROM debian:trixie-slim
WORKDIR /app
COPY --from=builder /app/fh-svc .
COPY --from=builder /app/fh-cli .
# Make sure to mount your config and the directory to monitor
CMD ["./fh-svc", "-config", "/config/config.yaml"]
