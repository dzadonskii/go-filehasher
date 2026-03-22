# go-filehasher

A high-performance File Integrity Monitor in Go.

## Features
- Real-time monitoring using `fsnotify` (inotify).
- Scheduled recursive scans.
- BLAKE3 hashing for speed.
- Merkle trees for directory-level integrity verification.
- SQLite database for persistent storage (WAL mode enabled).

## Binaries

The project consists of two binaries:

1. `fh-svc`: The background service that performs real-time monitoring and scheduled scans.
2. `fh-cli`: A user tool to interact with the database and trigger manual scans.

## Configuration

`fh-svc` uses a YAML configuration file:

```yaml
root_path: /path/to/monitor
db_path: /path/to/fh.db
scan_interval: 1h
batch_size: 1000  # Max files to hash per scan cycle (0 for unlimited)
db_commit_threshold: 1000  # Records per DB transaction commit (default: 1000)
```

## Usage

### Run the service

```bash
./fh-svc -config config.yaml
```

### CLI tool

```bash
# List all files and hashes
./fh-cli -config config.yaml list

# Trigger a manual scan
./fh-cli -config config.yaml -batch 1000 scan

# Remove non-existent entries from DB
./fh-cli -config config.yaml cleanup

# Verify files against DB
./fh-cli -config config.yaml check
```

## Deployment

### Systemd

Example unit file (`/etc/systemd/system/fh.service`):

```ini
[Unit]
Description=File Integrity Monitor Service
After=network.target

[Service]
ExecStart=/usr/local/bin/fh-svc -config /etc/go-filehasher/config.yaml
Restart=on-failure
User=root

[Install]
WantedBy=multi-user.target
```

### Docker

Example Dockerfile:

```dockerfile
FROM golang:1.25.8 AS builder
WORKDIR /app
COPY . .
RUN go build -o fh-svc cmd/fh-svc/main.go
RUN go build -o fh-cli cmd/fh-cli/main.go

FROM debian:bookworm-slim
WORKDIR /app
COPY --from=builder /app/fh-svc .
COPY --from=builder /app/fh-cli .
# Make sure to mount your config and the directory to monitor
CMD ["./fh-svc", "-config", "/config/config.yaml"]
```
