# claude-lens

A transparent HTTP proxy that sits between a Claude Code client and an
upstream Anthropic-compatible API. Every request and response is captured
and stored in a local SQLite database, giving you a full audit trail of
prompts, completions, token usage, and cost across sessions — with a small
admin dashboard to browse it.

`claude-lens` is a single static binary: no Docker, no separate runtime
install. It runs two HTTP servers concurrently in one process — a reverse
proxy and an admin UI — sharing one in-process database connection and
health flag.

---

## Getting started

**Prerequisites:** Go 1.26+.

### Development

```sh
cp .env.example .env    # fill in the values you need
go run -tags dev ./cmd/claude-lens
```

The `dev` build tag additionally reads `.env` from the working directory on
startup (any variable not already set in the real environment). This code
path doesn't exist at all in a release build — see [Configuration](#configuration).

### Release build

```sh
make build               # ./bin/claude-lens, current platform
make build-all            # linux/amd64, darwin/amd64, windows/amd64
```

or directly:

```sh
go build -o bin/claude-lens ./cmd/claude-lens
```

A release binary only ever reads real OS environment variables — set them
via shell `export`, a systemd unit's `Environment=`/`EnvironmentFile=`, or
similar. Run it from a working directory where `data/` and `logs/` can be
created (or point `DATA_DIR`/`LOG_DIR` at absolute paths, see below).

The **proxy** listens on `:7801` and the **admin UI** on `:7802` by
default; both are configurable.

---

## Configuration

All configuration is via environment variables — see `.env.example` for the
full list with defaults. None are required to start the binary.

| Variable | Default | Description |
|---|---|---|
| `PROXY_ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | Upstream API URL. Point this at a LiteLLM proxy or any other Anthropic-compatible endpoint. |
| `PROXY_ANTHROPIC_AUTH_TOKEN` | — | If set, replaces the `Authorization` header on every forwarded request. Accepts a raw key or a full `Scheme token` value. |
| `PROXY_ANTHROPIC_CUSTOM_HEADERS` | — | Extra headers injected into every forwarded request, one per line as `Header-Name: value`. |
| `PROXY_ADDR` | `:7801` | Proxy server listen address. |
| `ADMIN_ADDR` | `:7802` | Admin server listen address. |
| `DATA_DIR` | `data` | Where the SQLite database is created. |
| `LOG_DIR` | `logs` | Where the rotating log file is written (5MB × 3 backups). |

---

## Admin interface

### JSON API

| Endpoint | Description |
|---|---|
| `GET /health` | Admin liveness + in-process proxy status (`ok`/`degraded`/`unreachable`) |
| `GET /exchanges` | Paginated list of captured exchanges (`session_id`, `limit` ≤ 1000, `offset`) |
| `GET /exchanges/:id` | Full detail for one exchange, including raw request/response bodies |
| `DELETE /exchanges` | Delete all exchanges, or only those for a given `session_id` |
| `GET /totals` | Aggregate token/cost totals (`session_id` filter) |
| `GET /stream` | Server-Sent Events feed of live proxy status + totals, every 3s |
| `GET /session-stats` | Per-session aggregates |
| `GET /prices` | List all model price rows |
| `PUT /prices/:prefix` | Upsert a model price row (`input_per_m`, `output_per_m` JSON body) |
| `DELETE /prices/:prefix` | Remove a model price row |

### HTML dashboard

`GET /` (dashboard), `GET /ui/exchanges` (paginated table with session
filter), `GET /ui/exchanges/:id` (detail view), `GET /ui/prices` (manage
model prices — add/edit/delete, effective immediately, no restart needed).

Captured data lives in `<DATA_DIR>/claude-lens.db` (SQLite, WAL mode).

---

## How it works

Claude Code (and similar clients) sends every prompt as an HTTPS request to
the Anthropic API. `claude-lens` sits in that path: it receives the
request, forwards it upstream, tees the (possibly streamed) response back
to the client while buffering it, then saves the completed exchange —
without ever blocking or delaying what the client sees.

```
Claude Code  →  claude-lens proxy (:7801)  →  Upstream API
                       ↓
                 SQLite database  ←  claude-lens admin (:7802)
```

The client requires no changes — just point it at the proxy's address
instead of the real API.

### Using with a LiteLLM proxy

A common setup is to chain `claude-lens` in front of a LiteLLM proxy, to
route traffic through multiple models while still capturing everything the
client sends and receives:

```
Claude Code  →  claude-lens  →  LiteLLM Proxy  →  Anthropic / OpenAI / etc.
```

Set `PROXY_ANTHROPIC_BASE_URL` to your LiteLLM instance's address (e.g.
`http://litellm:4000`). If LiteLLM requires its own key, set
`PROXY_ANTHROPIC_AUTH_TOKEN` so the original client credentials are
transparently replaced.

### What gets captured

Only POST requests are intercepted (GET/PUT/DELETE pass through
untouched). For each one, the database records:

- Session ID (from `x-claude-code-session-id` or `x-session-id`) and
  session name (`x-session-name`, if set)
- The full message array sent by the client
- The assistant's response text
- `input_tokens`/`output_tokens` reported by the upstream API, and the
  estimated cost (longest-matching-prefix against the `Prices` table)
- The raw request and response bodies, for full-fidelity replay

Streaming responses are fully supported: each chunk is forwarded to the
client as it arrives (never buffered) while a copy is assembled for
storage once the stream completes.

---

## Development

```sh
go test ./...              # unit + integration tests
go build ./...              # compile everything
```

`make install-hooks` sets `core.hooksPath` to `.githooks` (installs a
post-commit hook that bumps `version` based on the commit message's
conventional-commit prefix).
