// Package schemaregistry handles downloading and caching of JSON schemas.
package schemaregistry

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// download fetches the raw bytes from a given URL with a strict timeout.
func download(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{
		Timeout: timeout,
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected HTTP status: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}
