package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseBlocklist(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"single", "example.com", []string{"example.com"}},
		{"multiple", "example.com,spam.net", []string{"example.com", "spam.net"}},
		{"whitespace", " example.com , spam.net ", []string{"example.com", "spam.net"}},
		{"strips www", "www.example.com", []string{"example.com"}},
		{"lowercases", "Example.COM", []string{"example.com"}},
		{"skips empty entries", "example.com,,spam.net,", []string{"example.com", "spam.net"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBlocklist(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsBlocked(t *testing.T) {
	saved := blocklist
	defer func() { blocklist = saved }()

	blocklist = []string{"spam.net", "evil.com"}

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"exact match", "https://spam.net/page", true},
		{"subdomain match", "https://sub.spam.net/page", true},
		{"www stripped", "https://www.spam.net/page", true},
		{"not blocked", "https://example.com/page", false},
		{"partial name not blocked", "https://notspam.net/page", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBlocked(tt.url); got != tt.want {
				t.Errorf("isBlocked(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}

	t.Run("empty blocklist", func(t *testing.T) {
		blocklist = nil
		if isBlocked("https://anything.com") {
			t.Error("expected false with empty blocklist")
		}
	})
}

func TestParseResults(t *testing.T) {
	// Minimal HTML that matches DDG's structure
	makeResult := func(url, title, snippet string) string {
		return `<div class="result results_links results_links_deep web-result">` +
			`<a class="result__a" href="` + url + `">` + title + `</a>` +
			`<a class="result__snippet">` + snippet + `</a>` +
			`<div class="clear"></div></div></div>`
	}

	t.Run("parses single result", func(t *testing.T) {
		html := makeResult("https://example.com", "Example", "A snippet")
		results := parseResults([]byte(html), 10)
		if len(results) != 1 {
			t.Fatalf("got %d results, want 1", len(results))
		}
		if results[0].URL != "https://example.com" {
			t.Errorf("URL = %q, want %q", results[0].URL, "https://example.com")
		}
		if results[0].Title != "Example" {
			t.Errorf("Title = %q, want %q", results[0].Title, "Example")
		}
		if results[0].Snippet != "A snippet" {
			t.Errorf("Snippet = %q, want %q", results[0].Snippet, "A snippet")
		}
	})

	t.Run("respects numResults limit", func(t *testing.T) {
		html := makeResult("https://a.com", "A", "a") + makeResult("https://b.com", "B", "b")
		results := parseResults([]byte(html), 1)
		if len(results) != 1 {
			t.Fatalf("got %d results, want 1", len(results))
		}
		if results[0].Title != "A" {
			t.Errorf("Title = %q, want %q", results[0].Title, "A")
		}
	})

	t.Run("unescapes HTML entities", func(t *testing.T) {
		html := makeResult("https://example.com", "Tom &amp; Jerry", "A &lt;great&gt; show")
		results := parseResults([]byte(html), 10)
		if len(results) != 1 {
			t.Fatalf("got %d results, want 1", len(results))
		}
		if results[0].Title != "Tom & Jerry" {
			t.Errorf("Title = %q, want %q", results[0].Title, "Tom & Jerry")
		}
		if results[0].Snippet != "A <great> show" {
			t.Errorf("Snippet = %q, want %q", results[0].Snippet, "A <great> show")
		}
	})

	t.Run("skips blocked domains", func(t *testing.T) {
		saved := blocklist
		defer func() { blocklist = saved }()
		blocklist = []string{"blocked.com"}

		html := makeResult("https://blocked.com", "Blocked", "x") +
			makeResult("https://ok.com", "OK", "y")
		results := parseResults([]byte(html), 10)
		if len(results) != 1 {
			t.Fatalf("got %d results, want 1", len(results))
		}
		if results[0].URL != "https://ok.com" {
			t.Errorf("URL = %q, want %q", results[0].URL, "https://ok.com")
		}
	})

	t.Run("empty body returns no results", func(t *testing.T) {
		results := parseResults([]byte(""), 10)
		if len(results) != 0 {
			t.Fatalf("got %d results, want 0", len(results))
		}
	})

	t.Run("converts bold tags to markdown and strips other tags", func(t *testing.T) {
		html := makeResult("https://example.com", "<b>Go</b> Programming", "Learn <b>Go</b> &amp; have fun")
		results := parseResults([]byte(html), 10)
		if len(results) != 1 {
			t.Fatalf("got %d results, want 1", len(results))
		}
		if results[0].Title != "**Go** Programming" {
			t.Errorf("Title = %q, want %q", results[0].Title, "**Go** Programming")
		}
		if results[0].Snippet != "Learn **Go** & have fun" {
			t.Errorf("Snippet = %q, want %q", results[0].Snippet, "Learn **Go** & have fun")
		}
	})

	t.Run("no matching blocks returns no results", func(t *testing.T) {
		results := parseResults([]byte("<html><body>no results here</body></html>"), 10)
		if len(results) != 0 {
			t.Fatalf("got %d results, want 0", len(results))
		}
	})
}

func TestFormatResults(t *testing.T) {
	t.Run("no results", func(t *testing.T) {
		got := formatResults("test query", nil)
		if got != "No results found for: test query" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("formats results with numbering", func(t *testing.T) {
		results := []SearchResult{
			{Title: "First", URL: "https://first.com", Snippet: "snippet one"},
			{Title: "Second", URL: "https://second.com", Snippet: "snippet two"},
		}
		got := formatResults("my query", results)

		if !strings.Contains(got, "Search results for: my query") {
			t.Error("missing header")
		}
		if !strings.Contains(got, "1. First") {
			t.Error("missing first result")
		}
		if !strings.Contains(got, "2. Second") {
			t.Error("missing second result")
		}
		if !strings.Contains(got, "https://first.com") {
			t.Error("missing first URL")
		}
		if !strings.Contains(got, "snippet two") {
			t.Error("missing second snippet")
		}
	})
}

func TestHandleToolCall_Validation(t *testing.T) {
	tests := []struct {
		name    string
		params  ToolCallParams
		wantErr string
	}{
		{
			name:    "unknown tool",
			params:  ToolCallParams{Name: "other"},
			wantErr: "unknown tool: other",
		},
		{
			name:    "missing query",
			params:  ToolCallParams{Name: "web_search", Arguments: map[string]any{}},
			wantErr: "missing required argument: query",
		},
		{
			name:    "empty query",
			params:  ToolCallParams{Name: "web_search", Arguments: map[string]any{"query": ""}},
			wantErr: "missing required argument: query",
		},
		{
			name: "query too long",
			params: ToolCallParams{
				Name:      "web_search",
				Arguments: map[string]any{"query": strings.Repeat("a", maxQueryLength+1)},
			},
			wantErr: "query too long",
		},
		{
			name: "num_results too low",
			params: ToolCallParams{
				Name:      "web_search",
				Arguments: map[string]any{"query": "test", "num_results": float64(0)},
			},
			wantErr: "num_results must be between",
		},
		{
			name: "num_results too high",
			params: ToolCallParams{
				Name:      "web_search",
				Arguments: map[string]any{"query": "test", "num_results": float64(maxResults + 1)},
			},
			wantErr: "num_results must be between",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handleToolCall(tt.params)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestIsRetryable(t *testing.T) {
	ddgRateLimitBody := `<link rel="canonical" href="https://duckduckgo.com/">`

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"context deadline", context.DeadlineExceeded, true},
		{"wrapped deadline", fmt.Errorf("request: %w", context.DeadlineExceeded), true},
		{"HTTP 202 with DDG homepage", &HTTPStatusError{StatusCode: http.StatusAccepted, Body: ddgRateLimitBody}, true},
		{"HTTP 202 without DDG homepage", &HTTPStatusError{StatusCode: http.StatusAccepted, Body: "accepted"}, false},
		{"HTTP 429", &HTTPStatusError{StatusCode: http.StatusTooManyRequests}, true},
		{"HTTP 500", &HTTPStatusError{StatusCode: http.StatusInternalServerError}, true},
		{"HTTP 503", &HTTPStatusError{StatusCode: http.StatusServiceUnavailable}, true},
		{"HTTP 403 (not retryable)", &HTTPStatusError{StatusCode: http.StatusForbidden}, false},
		{"HTTP 404 (not retryable)", &HTTPStatusError{StatusCode: http.StatusNotFound}, false},
		{"net.OpError (connection reset)", &net.OpError{Op: "read", Err: errors.New("connection reset")}, true},
		{"wrapped net.OpError", fmt.Errorf("request: %w", &net.OpError{Op: "dial", Err: errors.New("no route")}), true},
		{"DNS error", &net.DNSError{Err: "no such host", Name: "example.com"}, true},
		{"generic error (not retryable)", errors.New("unknown failure"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryable(tt.err); got != tt.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestHTTPStatusError(t *testing.T) {
	err := &HTTPStatusError{StatusCode: http.StatusAccepted, Body: "some body"}
	if err.Error() != "HTTP 202: some body" {
		t.Errorf("got %q", err.Error())
	}

	// Verify it works with errors.As
	wrapped := fmt.Errorf("search: %w", err)
	var target *HTTPStatusError
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As should find HTTPStatusError in wrapped error")
	}
	if target.StatusCode != http.StatusAccepted {
		t.Errorf("StatusCode = %d, want 202", target.StatusCode)
	}
}

// rewriteTransport redirects all requests to a test server URL,
// preserving the original request path, query, method and body.
type rewriteTransport struct {
	base      http.RoundTripper
	serverURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.serverURL, "http://")
	return t.base.RoundTrip(req)
}

// withTestServer starts a test HTTP server, redirects httpClient to it via
// rewriteTransport, and restores all global state when done.
func withTestServer(t *testing.T, handler http.Handler, fn func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()

	savedClient := httpClient
	savedDelay := searchDelay
	savedLastSearch := lastSearchAt
	defer func() {
		httpClient = savedClient
		searchDelay = savedDelay
		lastSearchAt = savedLastSearch
	}()

	httpClient = &http.Client{
		Transport: &rewriteTransport{base: server.Client().Transport, serverURL: server.URL},
	}
	searchDelay = 0
	lastSearchAt = time.Time{}

	fn()
}

func TestSearchDuckDuckGo_HTTPFlow(t *testing.T) {
	t.Run("non-200 status returns HTTPStatusError", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, "forbidden")
		})

		withTestServer(t, handler, func() {
			_, err := searchDuckDuckGo(context.Background(), "test", 5)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			var httpErr *HTTPStatusError
			if !errors.As(err, &httpErr) {
				t.Fatalf("expected HTTPStatusError, got %T: %v", err, err)
			}
			if httpErr.StatusCode != http.StatusForbidden {
				t.Errorf("StatusCode = %d, want 403", httpErr.StatusCode)
			}
			if !strings.Contains(httpErr.Body, "forbidden") {
				t.Errorf("Body = %q, want to contain 'forbidden'", httpErr.Body)
			}
		})
	})

	t.Run("DDG 202 rate limit returns retryable error", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprint(w, `<link rel="canonical" href="https://duckduckgo.com/">`)
		})

		withTestServer(t, handler, func() {
			_, err := searchDuckDuckGo(context.Background(), "test", 5)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !isRetryable(err) {
				t.Errorf("expected retryable error, got: %v", err)
			}
		})
	})

	t.Run("context timeout cancels request", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(500 * time.Millisecond):
			case <-r.Context().Done():
			}
		})

		withTestServer(t, handler, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			_, err := searchDuckDuckGo(ctx, "test", 5)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("expected DeadlineExceeded, got: %v", err)
			}
		})
	})

	t.Run("rate limit delay respects context cancellation", func(t *testing.T) {
		savedDelay := searchDelay
		savedLastSearch := lastSearchAt
		defer func() {
			searchDelay = savedDelay
			lastSearchAt = savedLastSearch
		}()

		searchDelay = 10 * time.Second
		lastSearchAt = time.Now()

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := searchDuckDuckGo(ctx, "test", 5)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("expected DeadlineExceeded, got: %v", err)
		}
		if elapsed > time.Second {
			t.Errorf("should have cancelled quickly, took %v", elapsed)
		}
	})
}
