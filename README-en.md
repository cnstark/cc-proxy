# cc_proxy

[![Release](https://img.shields.io/github/v/release/cnstark/cc-proxy?include_prereleases)](https://github.com/cnstark/cc-proxy/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/cnstark/cc-proxy)](https://goreportcard.com/report/github.com/cnstark/cc-proxy)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A local reverse proxy server that lets Claude Code connect to `127.0.0.1:8787` and routes requests to different upstream APIs by project.

**Key Features:** Model routing with primary/backup failover · Circuit breaker with tiered backoff · Hot reload (no restart) · Token usage statistics · Per-project isolation · Streaming SSE pass-through

## Table of Contents

- [Core Concepts](#core-concepts)
- [Quick Install](#quick-install)
- [Quick Start](#quick-start)
- [Configuration Reference](#configuration-reference)
- [Command Reference](#command-reference)
- [Feature Details](#feature-details)
  - [Model Routing & Failover](#model-routing--failover)
  - [allow_direct_access (Direct Model Name Access)](#allow_direct_access-direct-model-name-access)
  - [Circuit Breaker](#circuit-breaker)
  - [Hot Reload](#hot-reload)
  - [Token Usage Statistics](#token-usage-statistics)
  - [Log Levels](#log-levels)
- [Deployment & Operations](#deployment--operations)
  - [systemd Auto-Start](#systemd-auto-start)
  - [Docker Deployment](#docker-deployment)
  - [Windows Deployment](#windows-deployment)
- [Security](#security)
- [Tech Stack](#tech-stack)
- [Build from Source](#build-from-source)

---

## Core Concepts

cc_proxy revolves around **two types of configuration objects**:

- **Upstream (cfg)**: A globally shared API connection pool entry. Each upstream contains `{name, url, apikey, model, timeout, retry_backoff}`. The `model` field is the upstream's **real model name** — the proxy replaces the request body's model field with this value when forwarding.
- **Project**: Each project contains `{name, log_level, model_map}`. The `model_map` is a mapping of "Claude Code request model name (alias) → ordered upstream list." The list order determines primary/backup failover sequence. Projects are completely isolated from each other.

```
Claude Code                     cc_proxy                      Upstream API
┌──────────┐   127.0.0.1:8787   ┌──────────────┐  real model+key   ┌──────────────┐
│  model:   │ ────────────────→ │ Route→Rewrite │ ────────────────→ │ api.anthropic │
│ opus-4-8  │                   │ Stream resp.  │ ←──────────────── │ /v1/messages  │
│ key:sk-cp │ ←──────────────── │               │                   └──────────────┘
└──────────┘                   └──────────────┘
```

## Quick Install

### Linux / macOS

```bash
curl -fsSL https://github.com/cnstark/cc-proxy/releases/latest/download/install.sh | bash
source ~/.cc_proxy/env.sh
```

### Windows (PowerShell)

```powershell
irm https://github.com/cnstark/cc-proxy/releases/latest/download/install.ps1 | iex
& $env:USERPROFILE\.cc_proxy\env.ps1
```

### Docker

```bash
wget https://raw.githubusercontent.com/cnstark/cc-proxy/master/docker-compose.yml
docker-compose up -d
```

> First-time users: continue reading [Quick Start](#quick-start) below to configure upstreams and mappings.

---

## Quick Start

### 1. Generate a Private Key

```bash
ccp key gen
# → sk-cp-abcd1234...
```

### 2. Add an Upstream

```bash
ccp upstream add cfg1 \
  --url https://api.anthropic.com \
  --apikey sk-ant-xxx \
  --model claude-opus-4-8
```

### 3. Add a Project

```bash
ccp project add myproject --key sk-cp-abcd1234... --log-level meta
```

### 4. Add Model Mappings

```bash
ccp mapping add myproject claude-opus-4-8 cfg1 --backup cfg2
```

> `--backup` can be repeated to specify multiple fallback upstreams in failover order.

### 5. Start the Proxy

```bash
# Linux/macOS (background daemon)
ccp proxy start

# Windows (foreground)
ccp-proxy.exe
```

### 6. Configure Claude Code

In `~/.claude.json` or your project's `.claude/settings.json`:

```json
{
  "apiBaseUrl": "http://127.0.0.1:8787",
  "apiKey": "sk-cp-abcd1234..."
}
```

---

## Configuration Reference

The config file is located at `~/.cc_proxy/config.yaml` (customizable via the `CC_PROXY_CONFIG` environment variable). Full structure:

```yaml
server:
  listen: 127.0.0.1:8787          # Listen address (default, localhost only)
  usage_stats: false              # Enable token usage tracking (default: false)
  private_keys:                   # Private key → project name mapping
    sk-cp-abcd1234...: myproject
    sk-cp-efgh5678...: another-project

upstreams:
  - name: cfg1                    # Unique upstream identifier
    url: https://api.anthropic.com # API base URL
    apikey: sk-ant-xxx            # Upstream API key
    model: claude-opus-4-8        # Real model name (replaces request model when forwarding)
    timeout: 60s                  # Request timeout
    retry_backoff:                # Circuit breaker backoff tiers (optional, max 4; omit to disable)
      - 30s
      - 2m
      - 5m
      - 15m

  - name: cfg2
    url: https://api.openai.com
    apikey: sk-xxx
    model: gpt-5
    timeout: 120s
    # Omitting retry_backoff disables circuit breaking for this upstream

projects:
  - name: myproject
    log_level: meta               # off | meta | debug
    model_map:                    # Request model → ordered upstream list (primary → backup)
      claude-opus-4-8: [cfg1, cfg2]
      claude-sonnet-4-6: [cfg1]

  - name: another-project
    log_level: off
    model_map:
      claude-opus-4-8: [cfg2]
```

### Field Reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `server.listen` | string | No | Listen address, default `127.0.0.1:8787` |
| `server.usage_stats` | bool | No | Enable usage tracking, default `false` |
| `server.private_keys` | map | Yes | key→project mapping, at least one entry |
| `upstreams[].name` | string | Yes | Unique upstream identifier |
| `upstreams[].url` | string | Yes | API base URL |
| `upstreams[].apikey` | string | Yes | Upstream API key |
| `upstreams[].model` | string | Yes | Real model name on the upstream |
| `upstreams[].timeout` | duration | No | Request timeout, default `60s` |
| `upstreams[].retry_backoff` | []duration | No | Breaker backoff tiers, max 4; omit to disable |
| `projects[].name` | string | Yes | Unique project identifier |
| `projects[].log_level` | string | No | Log level: `off`/`meta`/`debug`, default `off` |
| `projects[].model_map` | map | Yes | Request model → upstream list |

---

## Command Reference

### Upstream Management

```bash
# Add an upstream
ccp upstream add <name> --url <url> --apikey <key> --model <model> [--timeout 60s]

# List all upstreams
ccp upstream list

# Remove an upstream
ccp upstream remove <name>

# Update an upstream (only specify fields to change)
ccp upstream update <name> [--url ...] [--apikey ...] [--model ...] [--timeout ...]
```

### Project Management

```bash
# Add a project
ccp project add <name> --key <private-key> [--log-level off|meta|debug]

# List all projects
ccp project list

# Remove a project (also removes associated private key)
ccp project remove <name>
```

### Model Mapping

```bash
# Add a mapping (can specify multiple backup upstreams)
ccp mapping add <project> <request-model> <primary-cfg> [--backup <backup-cfg>]...

# List mappings for a project
ccp mapping list <project>

# Remove a mapping
ccp mapping remove <project> <request-model>
```

### Key Management

```bash
# Generate a private key (prefixed sk-cp-)
ccp key gen
```

### Proxy Process Management

```bash
ccp proxy start       # Start in background (Linux/macOS only)
ccp proxy status      # Check running status
ccp proxy stop        # Stop (Linux/macOS only)
ccp proxy logs        # View logs
ccp proxy logs --project myproject --level debug   # Filter by project and level
```

### Usage Statistics Query

```bash
# All projects, last 7 days (default)
ccp stats

# Specific project
ccp stats myproject

# Custom time range
ccp stats --since 30d
ccp stats --since 2026-06-01

# Filter by model
ccp stats myproject --model claude-opus-4-8
```

---

## Feature Details

### Model Routing & Failover

Request processing pipeline:

1. **Authentication**: Extract key from `x-api-key` header (fallback: `Authorization: Bearer`), constant-time compare, resolve to project; miss → `401`
2. **Extract model**: Read full request body, JSON-parse the `model` field
3. **Route lookup**: Look up `model` in the project's `model_map` → ordered upstream list; miss → `404`
4. **Circuit breaker check**: Skip upstreams in backoff state (see [Circuit Breaker](#circuit-breaker))
5. **Failover loop**: Try each upstream in order:
   - Rewrite request body `model` to upstream's real model name
   - Replace `x-api-key` with upstream API key
   - Streaming POST to `<URL>/v1/messages`
   - Success → return; connection failure/timeout/5xx/429 → record failure and try next
6. **Streaming pass-through**: Uses `http.Flusher` to forward SSE response chunk by chunk (4KB buffer), without buffering the full response

> **Important**: Once the first byte is written to the client, failover is prohibited — streams cannot be merged after they've started.

### allow_direct_access (Direct Model Name Access)

When a project enables `allow_direct_access: true`, the `model` field in requests can directly use an upstream's `name` (cfg name) to route to that upstream, bypassing the `model_map` alias configuration.

**Constraint:** `model_map` aliases must not collide with any `upstream.name`, otherwise validation fails (ensuring unambiguous routing).

```yaml
projects:
  - name: default
    allow_direct_access: true
    model_map:
      claude-opus: [anthropic]
```

Toggle via `ccp project direct-access <name> <on|off>`.

### Circuit Breaker

The circuit breaker protects upstream services from being hammered during outages using tiered backoff:

**How it works:**

- **Trigger**: 2 consecutive failures on the same upstream → enter backoff
- **Backoff tiers**: Up to 4 tiers, wait time per tier defined by `retry_backoff` (e.g., `[30s, 2m, 5m, 15m]`)
- **State transitions**:
  - Normal (L0) → 2 consecutive failures → enter L1, wait 30s
  - L1 expires → allow one probe: success resets to normal, failure escalates to L2 (wait 2m)
  - Continue through L4; L4 failure cycles back to L1
- **Forced probe on all-skipped**: When every upstream in a model mapping is in backoff, the first upstream is forced to probe — prevents permanent lockout
- **Disabled by default**: Omit `retry_backoff` or leave it empty to bypass the breaker for that upstream

```yaml
# Example: configure retry_backoff in config.yaml
upstreams:
  - name: cfg1
    url: https://api.anthropic.com
    apikey: sk-ant-xxx
    model: claude-opus-4-8
    retry_backoff: [30s, 2m, 5m, 15m]
```

### Hot Reload

The proxy automatically detects changes to `~/.cc_proxy/config.yaml` at runtime and reloads without restarting:

- Polls file mtime every 2 seconds
- On change: re-parse YAML → validate → atomically replace runtime snapshot
- **Keeps old config on validation failure**, prints error to stderr, in-flight requests unaffected
- Each request rebuilds auth table, route table, and log level from the current snapshot — hot reload is naturally supported

### Token Usage Statistics

The proxy records token usage per request, aggregated by project/model/date for cost accounting and usage analysis. Disabled by **default**; must be manually enabled.

#### Enabling

Add `usage_stats: true` to the `server` section:

```yaml
server:
  listen: 127.0.0.1:8787
  usage_stats: true
  private_keys:
    sk-cp-abcd1234...: myproject
```

The switch supports **hot reload** — takes effect immediately without restarting. When turned off, no new records are generated, but historical data is preserved and can be resumed by re-enabling.

#### Data Storage

Usage data is persisted to `~/.cc_proxy/usage.json`:

- Loads historical data on startup, flushes to disk every 10 seconds in background, performs a final flush on exit
- Atomic writes (temp file + `rename`); dirty markers on flush failure for retry — no data loss
- Recording is fully transparent to request forwarding (fail-soft: malformed lines discarded, parse panics recovered)

#### Counting Semantics

- **Only counted when upstream returns usage fields** (parsed from Anthropic `message_start` / `message_delta` SSE events)
- **Failover only counts the successful upstream**: connection failures / timeouts / 5xx / 429 are not counted
- Archived to the date of **response completion**
- Tracks four dimensions: `input_tokens` / `output_tokens` / `cache_creation_input_tokens` / `cache_read_input_tokens`

#### Query

```bash
ccp stats                    # All projects, last 7 days
ccp stats myproject          # Specific project
ccp stats --since 30d        # Last 30 days
ccp stats --since 2026-06-01 # From a specific date
ccp stats myproject --model claude-opus-4-8  # Filter by model
```

Output columns: `PROJECT | MODEL | DATE | INPUT | OUTPUT | CACHE_CREATE | CACHE_READ | TOTAL`

> Displays `(No usage data)` when empty (no error if file doesn't exist); errors and exits if `usage.json` is corrupted.

### Log Levels

Each project can have an independent log level:

| Level | Description |
|-------|-------------|
| `off` | No logging (default) |
| `meta` | Request metadata: auth results, routing decisions, upstream selection, breaker state changes, token usage |
| `debug` | Everything in meta plus full request/response bodies (⚠️ contains API keys — switch back after troubleshooting) |

Logs are structured JSON, written to the proxy process's stderr (viewable via `ccp proxy logs`).

---

## Deployment & Operations

### systemd Auto-Start

```ini
# ~/.config/systemd/user/ccp-proxy.service
[Unit]
Description=cc_proxy proxy
After=network.target

[Service]
ExecStart=%h/bin/ccp-proxy
Restart=on-failure
Environment=CC_PROXY_CONFIG=%h/.cc_proxy/config.yaml

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now ccp-proxy
```

### Docker Deployment

```bash
# Download compose file
wget https://raw.githubusercontent.com/cnstark/cc-proxy/master/docker-compose.yml

# Start
docker-compose up -d

# View logs
docker-compose logs -f

# Run CLI commands inside the container
docker-compose exec ccp-proxy ccp key gen
docker-compose exec ccp-proxy ccp upstream add cfg1 ...

# Stop
docker-compose down
```

> Default entrypoint is `ccp-proxy`. Config file is persisted via volume mount to host `~/.cc_proxy`.
> You can also use the host `ccp` binary to manage the shared `config.yaml` (recommended, since the directory is mounted).
> Uses the `ghcr.io/cnstark/cc-proxy:latest` image, auto-pulled on first start.

### Windows Deployment

`ccp proxy start/stop` on Windows uses Unix signals and **cannot work properly**. Run in foreground instead:

```powershell
# Foreground
ccp-proxy.exe

# Or specify config path via environment variable
$env:CC_PROXY_CONFIG = "$env:USERPROFILE\.cc_proxy\config.yaml"
ccp-proxy.exe
```

Background options:

```powershell
# Option 1: Register as a Windows service with nssm
nssm install ccp-proxy "%USERPROFILE%\bin\ccp-proxy.exe"
nssm start ccp-proxy

# Option 2: Windows Scheduled Task
# Create a basic task triggered at system startup, action: start ccp-proxy.exe
```

---

## Security

- Config file contains upstream API keys; **must** `chmod 600 ~/.cc_proxy/config.yaml`
- `debug` log level writes credentials to disk; switch back to `meta` or `off` after troubleshooting
- Proxy **only listens on `127.0.0.1`**, never exposed to external network — this is a design constraint, do not change to `0.0.0.0`
- Authentication uses `crypto/subtle.ConstantTimeCompare` for timing-safe key comparison
- All error responses use Anthropic-compatible format, never leaking internal configuration details

---

## Tech Stack

- **Language**: Go 1.26
- **Dependencies**: `github.com/spf13/cobra` (CLI framework) + `gopkg.in/yaml.v3` (YAML parsing), standard library for everything else
- **Protocol**: Anthropic Messages API-compatible upstreams
- **Architecture**: Dual binary (`ccp` CLI management tool + `ccp-proxy` proxy daemon)

## Build from Source

```bash
git clone https://github.com/cnstark/cc-proxy.git
cd cc-proxy

# Linux/macOS
make build          # → bin/ccp, bin/ccp-proxy
make build-all      # Cross-compile for all platforms

# Windows
go build -o bin/ccp.exe ./cmd/ccp
go build -o bin/ccp-proxy.exe ./cmd/ccp-proxy

# Run tests
make test           # or go test ./...
```

> Requires Go 1.26+.
