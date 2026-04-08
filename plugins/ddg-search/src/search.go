package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// HTML parsing patterns for DuckDuckGo results.
// These depend on DDG's HTML structure and will break if it changes.
// If searches return 200 OK but zero results, these patterns are likely stale.
var (
	resultPattern  = regexp.MustCompile(`(?s)<div[^>]*class="[^"]*result[^"]*web-result[^"]*"[^>]*>.*?<div class="clear"></div>\s*</div>\s*</div>`)
	titlePattern   = regexp.MustCompile(`<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	snippetPattern = regexp.MustCompile(`<a[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
	htmlTagPattern = regexp.MustCompile(`<[^>]*>`)
)

type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// HTTPStatusError represents a non-200 HTTP response, carrying the status code
// so callers can decide whether to retry.
type HTTPStatusError struct {
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// isRetryable returns true for errors worth retrying: context timeouts,
// transient network errors (connection reset, DNS failure, TLS handshake),
// DDG rate limits (HTTP 202 with homepage redirect), HTTP 429, and server errors (5xx).
func isRetryable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode == http.StatusAccepted {
			// DDG returns 202 with its homepage when rate limiting.
			// Only retry if the body looks like the DDG homepage, not a legitimate 202.
			return strings.Contains(httpErr.Body, `rel="canonical" href="https://duckduckgo.com/"`)
		}
		return httpErr.StatusCode == http.StatusTooManyRequests ||
			httpErr.StatusCode >= http.StatusInternalServerError
	}
	return false
}

// cleanHTML converts <b> tags to markdown bold, strips remaining HTML tags,
// then collapses any resulting double-spaces.
func cleanHTML(s string) string {
	s = strings.ReplaceAll(s, "<b>", "**")
	s = strings.ReplaceAll(s, "</b>", "**")
	return htmlTagPattern.ReplaceAllString(s, "")
}

func searchDuckDuckGo(ctx context.Context, query string, numResults int) ([]SearchResult, error) {
	// Rate limit: wait if we searched too recently
	if searchDelay > 0 && !lastSearchAt.IsZero() {
		elapsed := time.Since(lastSearchAt)
		if wait := searchDelay - elapsed; wait > 0 {
			log.Printf("Rate limit: waiting %v before next search", wait.Round(time.Millisecond))
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	lastSearchAt = time.Now()

	searchURL := "https://html.duckduckgo.com/html/"

	data := url.Values{}
	data.Set("q", query)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, searchURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // response body read is complete; close errors are not actionable

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	// Limit response size to prevent OOM
	limitedReader := io.LimitReader(resp.Body, maxResponseSize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}

	// Check if there was more data beyond the limit (response was truncated)
	extra := make([]byte, 1)
	if n, _ := resp.Body.Read(extra); n > 0 {
		return nil, fmt.Errorf("response size exceeded maximum of %d bytes", maxResponseSize)
	}

	return parseResults(body, numResults), nil
}

func parseResults(body []byte, numResults int) []SearchResult {
	htmlContent := string(body)
	var results []SearchResult

	resultBlocks := resultPattern.FindAllString(htmlContent, -1)

	if len(resultBlocks) == 0 && len(body) > errorBodyLimit {
		log.Printf("Warning: no result blocks found in %d byte response — HTML structure may have changed", len(body))
	}

	for _, block := range resultBlocks {
		if len(results) >= numResults {
			break
		}

		titleMatch := titlePattern.FindStringSubmatch(block)
		snippetMatch := snippetPattern.FindStringSubmatch(block)

		if len(titleMatch) > 2 {
			resultURL := titleMatch[1]
			titleText := html.UnescapeString(cleanHTML(titleMatch[2]))

			if resultURL == "" {
				log.Printf("Warning: empty URL in result block, skipping")
				continue
			}
			if titleText == "" {
				log.Printf("Warning: empty title for URL %s, skipping", resultURL)
				continue
			}

			snippetText := ""
			if len(snippetMatch) > 1 {
				snippetText = html.UnescapeString(cleanHTML(snippetMatch[1]))
			}

			if isBlocked(resultURL) {
				continue
			}

			results = append(results, SearchResult{
				Title:   titleText,
				URL:     resultURL,
				Snippet: snippetText,
			})
		} else {
			log.Printf("Warning: failed to extract title from result block")
		}
	}

	return results
}

func isBlocked(urlStr string) bool {
	if len(blocklist) == 0 {
		return false
	}

	parsed, err := url.Parse(urlStr)
	if err != nil {
		log.Printf("Warning: failed to parse URL for blocklist check: %s: %v", urlStr, err)
		return false
	}

	domain := strings.ToLower(parsed.Hostname())
	domain = strings.TrimPrefix(domain, "www.")

	for _, blocked := range blocklist {
		if domain == blocked {
			return true
		}
		if strings.HasSuffix(domain, "."+blocked) {
			return true
		}
	}

	return false
}

func formatResults(query string, results []SearchResult) string {
	if len(results) == 0 {
		return "No results found for: " + query
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Search results for: %s\n\n", query)

	for i, result := range results {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n\n", i+1, result.Title, result.URL, result.Snippet)
	}

	return sb.String()
}
