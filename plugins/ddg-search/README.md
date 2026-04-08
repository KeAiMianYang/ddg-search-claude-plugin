# DuckDuckGo Search MCP Server

Fast DuckDuckGo web search MCP server with **instant startup** (~20ms) and zero runtime dependencies.

## Features

- Web search via DuckDuckGo HTML interface
- Proxy support (respects `HTTP_PROXY`/`HTTPS_PROXY` environment variables)
- Domain blocklist to filter unwanted results
- Rate limiting with configurable delay between searches
- Automatic retries on timeouts, server errors (429, 5xx), and DuckDuckGo rate limits (HTTP 202 with homepage redirect)
- Configurable search timeout
- Returns up to 20 results per search
- Instant startup with pre-compiled binaries
- No external dependencies required

## Installation

### As a Claude Code Plugin

```
claude plugin marketplace add https://github.com/KeAiMianYang/ddg-search-claude-plugin
claude plugin install ddg-search@ddg-search
```

Pre-compiled binaries for all supported platforms are included. The `launcher.sh` script automatically detects your platform and runs the correct binary.

A bundled hook automatically blocks the built-in `WebSearch` tool and redirects the agent to use `mcp__plugin_ddg-search_ddg-search__web_search` instead. To avoid the agent attempting `WebSearch` in the first place, add this to your CLAUDE.md:

```markdown
# Web Search

Always use `mcp__plugin_ddg-search_ddg-search__web_search` for web searches.
```

### As a Standalone MCP Server

Reference the launcher in your MCP configuration:

```json
{
  "mcpServers": {
    "ddg-search": {
      "type": "stdio",
      "command": "/absolute/path/to/ddg-search/launcher.sh",
      "env": {
        "BLOCKLIST": "",
        "SEARCH_TIMEOUT": "10",
        "SEARCH_DELAY": "2",
        "MAX_RETRIES": "3",
        "RETRY_DELAY": "5"
      }
    }
  }
}
```

## Building from Source

### Prerequisites

- Go 1.26 or later

### Build All Platforms

```bash
./build.sh
```

This creates binaries for:

- macOS (Intel and Apple Silicon)
- Linux (x64 and ARM64)

Binaries are placed in `packages/ddg-search-<platform>-<arch>/` directories.

## Environment Variables

- `BLOCKLIST`: Comma-separated list of domains to exclude (e.g., `"example.com,spam.net"`)
- `MAX_RETRIES`: Maximum number of retries on timeout or server error (default: `3`). Set to `0` to disable.
- `RETRY_DELAY`: Delay between retries in seconds (default: `5`)
- `SEARCH_DELAY`: Minimum delay between searches in seconds (default: `2`). Set to `0` to disable.
- `SEARCH_TIMEOUT`: Search timeout in seconds (default: `10`)
- `HTTP_PROXY` / `HTTPS_PROXY`: Proxy server URL (automatically detected)

## Tool Schema

### `web_search`

Search the web using DuckDuckGo.

**Input:**

```json
{
  "query": "search terms",
  "num_results": 10
}
```

**Parameters:**

- `query` (string, required): Search query
- `num_results` (integer, optional): Number of results (1-20, default: 10)

**Output:**
Formatted text with search results including titles, URLs, and snippets.

## Architecture

This implementation bundles pre-compiled binaries with a platform launcher:

1. **Launcher script** (`launcher.sh`): Detects OS and architecture, executes correct binary
2. **Platform binaries**: In `packages/ddg-search-<platform>-<arch>/` directories
3. **Single binary per platform**: No runtime dependencies

Benefits:

- Instant startup (~20ms)
- Zero installation complexity
- Works offline - all binaries bundled
- Simple shell script dispatching

## Supported Platforms

- macOS (Intel): darwin-x64
- macOS (Apple Silicon): darwin-arm64
- Linux (x64): linux-x64
- Linux (ARM64): linux-arm64
- Windows: Use WSL or Git Bash with linux-x64 binary

## Troubleshooting

### "Binary not found for platform X-Y"

Run `./build.sh` to build binaries for all platforms. The launcher requires pre-built binaries in `packages/` directories.

### Permission denied

Ensure launcher and binaries are executable:

```bash
chmod +x launcher.sh
chmod +x packages/*/ddg-search
```

## License

MIT
