package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// withRetry wraps an HTTP call that may fail, retrying with exponential backoff on failure.
//
//   - maxRetries = 0 means infinite retry (for memory extraction / embedding batch jobs)
//   - maxRetries > 0 means at most N retries
//   - initial backoff 2s, ×1.5 each time, capped at 60s
//   - timeout / connection error / 5xx / 429 all trigger retry
//   - 4xx (except 429) does not retry
//
// fn receives the current attempt number (0 = first), returns response or error.
// On success, caller is responsible for closing resp.Body.
func withRetry(maxRetries int, fn func(attempt int) (*http.Response, error)) (*http.Response, error) {
	const (
		initialBackoff = 2 * time.Second
		maxBackoff     = 60 * time.Second
		backoffFactor  = 1.5
	)

	backoff := initialBackoff
	var lastErr error

	for attempt := 0; maxRetries <= 0 || attempt <= maxRetries; attempt++ {
		resp, err := fn(attempt)
		if err == nil {
			if resp.StatusCode >= 500 || resp.StatusCode == 429 {
				resp.Body.Close()
				lastErr = fmt.Errorf("API 错误 %d", resp.StatusCode)
			} else {
				return resp, nil
			}
		} else {
			lastErr = err
		}

		// 4xx (except 429) — don't retry
		shouldRetry := true
		if lastErr != nil {
			msg := lastErr.Error()
			if strings.Contains(msg, "API 错误 4") && !strings.Contains(msg, "429") {
				shouldRetry = false
			}
		}

		if !shouldRetry {
			return nil, lastErr
		}

		// Exponential backoff
		if attempt < maxRetries || maxRetries <= 0 {
			time.Sleep(backoff)
			backoff = time.Duration(float64(backoff) * backoffFactor)
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}

	return nil, lastErr
}
