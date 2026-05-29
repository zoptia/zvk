package main

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// httpClient is a shared client with a sane timeout — release index lookups
// should fail fast, while tarball downloads need a longer per-request budget.
var httpClient = &http.Client{
	Timeout: 5 * time.Minute,
}

// downloadToMemory streams the response body for `url` into memory.
// The User-Agent identifies the tool so GitHub's API returns JSON instead of HTML.
func downloadToMemory(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "zvk/"+zvkVersion)
	req.Header.Set("Accept", "application/octet-stream, application/json, */*")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
