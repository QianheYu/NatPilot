package netutil

import (
	"context"
	"io"
	"net/http"
	"time"
)

var httpClient = &http.Client{Timeout: 6 * time.Second}

// httpGet performs a bounded GET request.
func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "NatPilot/0.1")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 4096))
}
