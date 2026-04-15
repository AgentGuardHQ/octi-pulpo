package bootcheck

import (
	"context"
	"net/http"
	"time"
)

func defaultHTTPGet(ctx context.Context, url, token string) (int, error) {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("User-Agent", "octi-pulpo-bootcheck")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
