package main

import (
	"context"
	"net/http"
	"time"
)

// backendStatusUp 探测一次 WeLink 后端 /api/status 是否可达。
func backendStatusUp(ctx context.Context, cfg *Config) bool {
	if cfg == nil {
		return false
	}
	ctxT, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctxT, http.MethodGet, cfg.WeLinkBaseURL+"/api/status", nil)
	if err != nil {
		return false
	}
	if cfg.WeLinkToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.WeLinkToken)
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
