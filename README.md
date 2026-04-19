# log-sentinel

A terminal UI that watches your application logs, classifies errors with an LLM, deduplicates them into tickets, and dispatches workers to investigate and fix them automatically.

```
┌─ Log Sentinel ──────────────────────────── Open:3  Active:1  Done:7 ─┐
│ [1] Overview   [2] Logs   [3] Tickets (11)   [4] Workers              │
├────────────────────────────────────────────────────────────────────────┤
│ [f] Status: All   [a] App: all   [s] Sev: all                         │
│                                                                        │
│ ▶ [CRIT] [TODO] payments-api   ×3  #8                                 │
│   database_connection_timeout  payments  2m ago                       │
│ ···············································                        │
│   [HIGH] [WORK] inventory-service  ×1  #7                             │
│   null_pointer_exception  catalog  5m ago                             │
│ ···············································                        │
└────────────────────────────────────────────────────────────────────────┘
```

## How it works

1. **Pollers** query GCP Cloud Logging or AWS CloudWatch on a configurable interval
2. **Classifier** sends each error log to AWS Bedrock (Nova micro by default) which returns a structured classification and a normalized fingerprint for deduplication
3. **Store** upserts tickets into SQLite — same fingerprint on the same day increments the occurrence counter; if today's ticket is already closed it is skipped entirely
4. **Queue** publishes new tickets to AWS SQS
5. **Workers** long-poll SQS and run your configured actions: call a webhook, or spawn any CLI agent (including Claude Code)
6. **TUI** shows everything live: log stream, ticket list with filters, worker activity, and ticket↔worker status linkage

## Requirements

- Go 1.21+
- Docker (for local mode with ministack)
- AWS credentials (for real mode — Bedrock + SQS + optionally CloudWatch)
- GCP credentials (only if using GCP log sources)

## Quick start (local mock mode)

No AWS account needed. Uses ministack for local SQS and a rule-based classifier.

```bash
./run-mock.sh
```

This will:
1. Start a local SQS via Docker (`ministackorg/ministack`)
2. Create the `log-sentinel` queue
3. Build and run the app with `config.mock.yaml`

## TUI navigation

| Key | Action |
|-----|--------|
| `1` `2` `3` `4` | Jump to tab |
| `Tab` / `Shift+Tab` | Cycle tabs |
| `↑` `↓` / `j` `k` | Scroll / move cursor |
| `g` / `G` | Top / bottom |
| `Enter` | Open ticket detail |
| `Esc` | Back |
| `f` | Cycle status filter (Tickets tab) |
| `a` | Cycle app filter |
| `s` | Cycle severity filter (Tickets tab) |
| `q` / `Ctrl+C` | Quit |

## Configuration

Copy `config.example.yaml` and edit for your environment:

```yaml
aws:
  region: us-east-1
  bedrock_model: amazon.nova-micro-v1:0   # or anthropic.claude-haiku-4-5
  sqs_queue_url: https://sqs.us-east-1.amazonaws.com/123456/log-sentinel

applications:
  - name: my-api
    source: gcp
    project: my-gcp-project
    filter: 'resource.type="k8s_container" severity>=ERROR'
    poll_interval: 60s

  - name: payments
    source: aws
    region: us-east-1
    log_group: /ecs/payments
    filter_pattern: "ERROR"
    poll_interval: 30s
```

### Log sources

| `source` | Required fields |
|----------|-----------------|
| `gcp` | `project`, `filter` |
| `aws` | `log_group`, `filter_pattern`, `region` |
| `mock` | — (generates random errors, useful for testing) |

### Workers

Workers long-poll the SQS queue and run actions sequentially. If any action fails, the ticket is left in the queue for redelivery with a backoff.

```yaml
workers:
  - app: payments-api
    concurrency: 2
    actions:
      - type: webhook
        name: notify-slack
        url: https://hooks.slack.com/services/...
        secret: my-hmac-secret   # optional HMAC-SHA256 X-Signature-256 header

      - type: cli_agent
        name: claude-debug
        command: claude
        args: ["--print", "--dangerously-skip-permissions"]
        prompt_mode: arg
        working_dir: /path/to/repo
        timeout: 300s
```

#### Action types

**`webhook`** — POSTs the ticket as JSON. Optional `secret` adds an HMAC-SHA256 `X-Signature-256` header for verification.

**`cli_agent`** — Spawns any command with the ticket rendered into a Go template prompt. Use `prompt_mode: arg` to pass as a CLI argument or `prompt_mode: stdin` to write to stdin.

The default prompt template includes app name, component, severity, classification, fingerprint, and raw log line. Override with `prompt_template`.

#### Using Claude Code as the agent

```yaml
- type: cli_agent
  name: claude-debug
  command: claude
  args: ["--print", "--dangerously-skip-permissions"]
  prompt_mode: arg
  working_dir: /path/to/your/repo
  timeout: 300s
```

## Running

```bash
go run ./cmd/sentinel --config config.yaml --db log-sentinel.db
```

Logs are written to `log-sentinel.log` to avoid interfering with the TUI.

## Architecture

```
GCP / AWS / Mock
      │
   [Poller] ──► [Classifier / Bedrock]
      │
   [SQLite Store] ── dedup by (app, fingerprint_hash, date)
      │
   [SQS Queue]
      │
   [Worker] ──► webhook / cli_agent
      │
   [TUI] ◄── channels (logs, tickets, status, worker events)
```

## Development

```bash
# Run tests
go test ./...

# Build
go build -o log-sentinel ./cmd/sentinel

# Upgrade dependencies
go get -u ./...
go mod tidy
```
