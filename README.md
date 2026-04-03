# version-to-hash

Converts GitHub Actions version tags to pinned commit hashes, protecting against **tag-shifting attacks**.

## The problem

GitHub Actions workflows commonly reference actions by version tag:

```yaml
- uses: actions/checkout@v4
- uses: actions/setup-python@v5
```

Git tags are mutable. An attacker who compromises an action's repository (or its maintainer account) can silently redirect `v4` to point at malicious code. Every workflow using `@v4` then executes that code with full runner privileges.

The fix is to pin by commit hash instead:

```yaml
- uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4
- uses: actions/setup-python@0b93645e9fea7318ecaed2b359559ac225c90a2b # v5
```

Hashes are immutable. This tool automates the conversion.

## Features

- **MCP server** — Claude can query it directly to pin or upgrade actions while editing workflows
- **REST API** — resolve or upgrade actions over HTTP from scripts or other tools
- **Disk cache** — BoltDB cache avoids redundant GitHub API calls; shared across sessions
- **Annotated tag support** — correctly dereferences multi-level tag objects to the actual commit SHA

## Connecting to Claude

There are two ways to run the tool. Choose whichever fits your setup.

### Option A — Command (stdio)

The binary runs as a subprocess, launched on demand by Claude. No server to manage.

```bash
# Build and put the binary on your PATH
task build
cp bin/version-to-hash ~/bin/version-to-hash   # or anywhere on $PATH
```

Add the following to `~/.claude.json`:

```json
{
  "mcpServers": {
    "version-to-hash": {
      "type": "stdio",
      "command": "/Users/yourname/bin/version-to-hash",
      "args": ["--stdio"]
    }
  }
}
```

Or use the CLI instead of editing the file directly:

```bash
claude mcp add --transport stdio version-to-hash -- /Users/yourname/bin/version-to-hash --stdio
```

The cache is stored at `~/.config/version-to-hash/bolt.db` (macOS: `~/Library/Application Support/version-to-hash/bolt.db`) and is shared across all sessions. If two Claude instances start simultaneously and both try to open the cache, the second one gracefully falls back to hitting the GitHub API directly without caching.

### Option B — HTTP server

Run a persistent server, useful if you want the REST API as well, or want a single cache shared across users or machines.

```bash
# With Docker Compose (recommended)
docker compose up -d

# Or run the binary directly
version-to-hash   # listens on :8080
```

Add the following to `~/.claude.json`:

```json
{
  "mcpServers": {
    "version-to-hash": {
      "type": "http",
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

Or use the CLI:

```bash
claude mcp add --transport http version-to-hash http://localhost:8080/mcp
```

Once connected either way, open a new Claude Code session and run `/mcp` to confirm `version-to-hash` appears as connected.

Two MCP tools are available:

| Tool | Input | What it does |
|------|-------|--------------|
| `resolve_github_action` | `owner/repo@version` | Resolves a specific tag to its pinned commit hash |
| `upgrade_github_action` | `owner/repo` | Finds the latest release and returns its pinned commit hash |

Ask Claude to "pin all actions in this workflow" to use `resolve_github_action`, or "upgrade all actions to their latest versions" to use `upgrade_github_action`.

## REST API

Only available in HTTP server mode (Option B).

### `GET /health`

Returns `{"status":"ok"}` — useful for liveness probes.

### `GET /resolve?action=<ref>`

| Parameter | Description |
|-----------|-------------|
| `action` | Action reference in `owner/repo@version` format |

### `POST /resolve`

Accepts a JSON body: `{ "action": "actions/setup-python@v5" }`

Response fields:

| Field | Description |
|-------|-------------|
| `action` | Original input |
| `resolved` | Full pinned reference ready to paste into a workflow |
| `hash` | The 40-character commit SHA |
| `cached` | Whether the result came from the local cache |

Example response:

```json
{
  "action": "actions/checkout@v4",
  "resolved": "actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683",
  "hash": "11bd71901bbe5b1630ceea73d27597364c9af683",
  "cached": false
}
```

### `GET /latest?action=<owner/repo>`

| Parameter | Description |
|-----------|-------------|
| `action` | Action in `owner/repo` format (version suffix is ignored if present) |

### `POST /latest`

Accepts a JSON body: `{ "action": "actions/checkout" }`

Finds the latest published release (pre-releases excluded) and returns its pinned hash. Response fields:

| Field | Description |
|-------|-------------|
| `action` | Canonical `owner/repo` |
| `tag` | Latest release tag (e.g. `v4.2.2`) |
| `resolved` | Full pinned reference ready to paste into a workflow |
| `hash` | The 40-character commit SHA |
| `cached` | Whether the result came from the local cache |

Example response:

```json
{
  "action": "actions/checkout",
  "tag": "v4.2.2",
  "resolved": "actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683",
  "hash": "11bd71901bbe5b1630ceea73d27597364c9af683",
  "cached": false
}
```

## Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP listen port (server mode only) |
| `CACHE_PATH` | `~/.config/version-to-hash/bolt.db` | Path to the BoltDB cache file |
| `CACHE_TTL_HOURS` | `24` | How long resolved hashes are cached before re-fetching |
| `GITHUB_TOKEN` | _(none)_ | GitHub personal access token. Without one the API is rate-limited to 60 req/hr per IP; with one, 5000/hr. |
| `LOG_LEVEL` | `info` | Logrus level: `debug`, `info`, `warn`, `error` |

## Local development

Prerequisites: Go 1.23+, [Task](https://taskfile.dev)

```bash
task test          # run all tests
task build         # build binary to ./bin/
task run           # build + run HTTP server locally on :8080
task lint          # go vet
task demo          # build, start server, resolve actions/checkout@v4, stop
task docker:build  # build Docker image
task docker:run    # build + run container on :8080
```

See [Taskfile.yml](./Taskfile.yml) for all available tasks.

## Docker

```bash
# Build
docker build -t version-to-hash .

# Run (cache persists in a named volume)
docker run -d \
  -p 8080:8080 \
  -v vth-cache:/data \
  version-to-hash
```

The container image is also published to GitHub Container Registry on every push to `main`:

```bash
docker pull ghcr.io/OWNER/version-to-hash:latest
```

## Architecture

```
cmd/server/main.go          — entry point; --stdio flag selects transport
internal/
  cache/bolt.go             — BoltDB key/value store with TTL; no-op fallback when locked
  github/client.go          — GitHub REST API client; handles lightweight and annotated tags
  api/handler.go            — Gin REST handlers for /resolve and /health
  mcp/server.go             — MCP server, exposes resolve_github_action tool
```

The REST API and MCP endpoint share the same cache and GitHub client instance.
