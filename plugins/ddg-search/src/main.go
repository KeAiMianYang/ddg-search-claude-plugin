package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	maxResults         = 20
	defaultTimeout     = 10
	defaultSearchDelay = 2 // seconds between searches to avoid rate limits
	defaultMaxRetries  = 3
	defaultRetryDelay  = 5 // seconds between retries
	maxQueryLength     = 2048
	maxResponseSize    = 10 * 1024 * 1024 // 10 MB

	defaultMaxIdleConns = 10
	idleConnTimeoutSec  = 90
	defaultNumResults   = 10
	errorBodyLimit      = 1024
)

// Configuration loaded at startup
var (
	searchTimeout time.Duration
	searchDelay   time.Duration
	maxRetries    int
	retryDelay    time.Duration
	blocklist     []string
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"
var (
	lastSearchAt time.Time
	httpClient   = &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        defaultMaxIdleConns,
			MaxIdleConnsPerHost: defaultMaxIdleConns,
			IdleConnTimeout:     idleConnTimeoutSec * time.Second,
		},
	}
)

// MCP Protocol types

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      any         `json:"id"`
	Result  any         `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
}

type Capabilities struct {
	Tools map[string]bool `json:"tools,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema any `json:"inputSchema"`
}

type ToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolResult struct {
	Content []TextContent `json:"content"`
}

func init() {
	searchTimeout = loadEnvDuration("SEARCH_TIMEOUT", defaultTimeout, 1)
	searchDelay = loadEnvDuration("SEARCH_DELAY", defaultSearchDelay, 0)
	retryDelay = loadEnvDuration("RETRY_DELAY", defaultRetryDelay, 0)
	maxRetries = loadEnvInt("MAX_RETRIES", defaultMaxRetries, 0)
	blocklist = parseBlocklist(os.Getenv("BLOCKLIST"))
}

// loadEnvInt reads an integer from an environment variable, returning the
// default if unset, invalid, or below minVal.
func loadEnvInt(name string, defaultVal, minVal int) int {
	s := os.Getenv(name)
	if s == "" {
		return defaultVal
	}
	parsed, err := strconv.Atoi(s)
	if err != nil {
		log.Printf("Warning: invalid %s value '%s', using default %d", name, s, defaultVal)
		return defaultVal
	}
	if parsed < minVal {
		log.Printf("Warning: %s must be >= %d, using default %d", name, minVal, defaultVal)
		return defaultVal
	}
	return parsed
}

// loadEnvDuration is like loadEnvInt but returns the value as seconds.
func loadEnvDuration(name string, defaultSec, minVal int) time.Duration {
	return time.Duration(loadEnvInt(name, defaultSec, minVal)) * time.Second
}

func parseBlocklist(blocklistStr string) []string {
	if blocklistStr == "" {
		return nil
	}

	var domains []string
	for domain := range strings.SplitSeq(blocklistStr, ",") {
		domain = strings.TrimSpace(strings.ToLower(domain))
		if domain != "" {
			domain = strings.TrimPrefix(domain, "www.")
			domains = append(domains, domain)
		}
	}
	return domains
}

func main() {
	log.SetOutput(os.Stderr)
	log.Println("DuckDuckGo MCP Server starting...")

	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)

	for {
		var req JSONRPCRequest
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("Error decoding request: %v", err)

			errorResp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &RPCError{Code: -32700, Message: "parse error"},
			}
			if encErr := encoder.Encode(errorResp); encErr != nil {
				log.Fatalf("Fatal: error encoding error response: %v", encErr)
			}
			continue
		}

		log.Printf("Received request: %s", req.Method)

		if strings.HasPrefix(req.Method, "notifications/") {
			log.Printf("Ignoring notification: %s", req.Method)
			continue
		}

		var response JSONRPCResponse
		response.JSONRPC = "2.0"
		response.ID = req.ID

		switch req.Method {
		case "initialize":
			response.Result = InitializeResult{
				ProtocolVersion: "2024-11-05",
				Capabilities: Capabilities{
					Tools: map[string]bool{"listChanged": false},
				},
				ServerInfo: ServerInfo{
					Name:    "ddg-search",
					Version: version,
				},
			}

		case "tools/list":
			response.Result = map[string]any{
				"tools": []Tool{
					{
						Name:        "web_search",
						Description: "Search the web using DuckDuckGo. Returns a list of search results with titles, URLs, and snippets. Use this to find current information, documentation, articles, or any web content.",
						InputSchema: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"query": map[string]any{
									"type":        "string",
									"description": "The search query",
								},
								"num_results": map[string]any{
									"type":        "integer",
									"description": "Maximum number of results to return (default: 10)",
									"default":     defaultNumResults,
									"minimum":     1,
									"maximum":     maxResults,
								},
							},
							"required": []string{"query"},
						},
					},
				},
			}

		case "tools/call":
			var params ToolCallParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				response.Error = &RPCError{Code: -32602, Message: "invalid params"}
			} else {
				result, err := handleToolCall(params)
				if err != nil {
					response.Error = &RPCError{Code: -32603, Message: err.Error()}
				} else {
					response.Result = result
				}
			}

		default:
			response.Error = &RPCError{Code: -32601, Message: "method not found"}
		}

		if err := encoder.Encode(response); err != nil {
			log.Fatalf("Fatal: error encoding response: %v", err)
		}
	}
}

func handleToolCall(params ToolCallParams) (ToolResult, error) {
	if params.Name != "web_search" {
		return ToolResult{}, fmt.Errorf("unknown tool: %s", params.Name)
	}

	query, ok := params.Arguments["query"].(string)
	if !ok || query == "" {
		return ToolResult{}, fmt.Errorf("missing required argument: query")
	}

	if len(query) > maxQueryLength {
		return ToolResult{}, fmt.Errorf("query too long: maximum %d characters", maxQueryLength)
	}

	numResults := defaultNumResults
	if nr, ok := params.Arguments["num_results"].(float64); ok {
		numResults = int(nr)
	}

	if numResults < 1 || numResults > maxResults {
		return ToolResult{}, fmt.Errorf("num_results must be between 1 and %d", maxResults)
	}

	var lastErr error
	for attempt := range maxRetries + 1 {
		if attempt > 0 {
			log.Printf("Retry %d/%d: waiting %v before retrying", attempt, maxRetries, retryDelay)
			time.Sleep(retryDelay)
		}

		ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
		results, err := searchDuckDuckGo(ctx, query, numResults)
		cancel()

		if err == nil {
			output := formatResults(query, results)
			return ToolResult{
				Content: []TextContent{
					{Type: "text", Text: output},
				},
			}, nil
		}

		lastErr = err
		if !isRetryable(err) {
			break
		}
		log.Printf("Search failed (attempt %d/%d): %v", attempt+1, maxRetries+1, err)
	}

	return ToolResult{}, fmt.Errorf("search failed: %w", lastErr)
}
