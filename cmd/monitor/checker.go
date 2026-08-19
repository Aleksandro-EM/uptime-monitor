package main

import (
	"io"
	"net/http"
	"time"
)

// target is one thing to check: a URL plus any headers to send with every
// request to it (e.g. an auth key a protected endpoint like /health needs).
type target struct {
	URL     string
	Headers map[string]string
}

// checkResult holds the outcome of a single HTTP health check.
type checkResult struct {
	URL        string
	StatusCode int
	Latency    time.Duration
	Err        error
	// Body is the response body, captured only when the check failed
	// (StatusCode >= 400) and truncated to maxCapturedBodyBytes, so a target
	// that reports its own detailed status (like a JSON /health endpoint)
	// can surface *why* it's unhealthy, not just that it is.
	Body string
}

const maxCapturedBodyBytes = 2048

// checkOnce performs a single blocking GET request against t.URL, with
// t.Headers attached, and reports how long it took and whether it
// succeeded.
func checkOnce(client *http.Client, t target) checkResult {
	start := time.Now()

	req, err := http.NewRequest(http.MethodGet, t.URL, nil)
	if err != nil {
		return checkResult{URL: t.URL, Latency: time.Since(start), Err: err}
	}
	for name, value := range t.Headers {
		req.Header.Set(name, value)
	}

	resp, err := client.Do(req)
	latency := time.Since(start)

	if err != nil {
		return checkResult{URL: t.URL, Latency: latency, Err: err}
	}
	defer resp.Body.Close()

	result := checkResult{URL: t.URL, StatusCode: resp.StatusCode, Latency: latency}

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxCapturedBodyBytes))
		result.Body = string(body)
	}

	return result
}

// runChecks fires one goroutine per target and streams results back over a
// channel as each check completes, rather than waiting for them in order.
func runChecks(client *http.Client, targets []target) <-chan checkResult {
	results := make(chan checkResult, len(targets))

	for _, t := range targets {
		go func(t target) {
			results <- checkOnce(client, t)
		}(t)
	}

	return results
}
