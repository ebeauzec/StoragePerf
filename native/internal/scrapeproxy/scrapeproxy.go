// Package scrapeproxy implements the authenticated fetch of one Pure
// array's native /metrics on Prometheus's behalf, so the per-array bearer
// token lives only here — either in config/arrays.yml for a real array, or
// in internal/mockbackend's own address for a mock one — never in
// Prometheus's own config, and never over an unauthenticated hop.
package scrapeproxy

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"plumb/internal/config"
)

// Lookup resolves an array ID to its config — real arrays.yml while mock
// mode is off, the mock fleet (pointed at internal/mockbackend) while it's
// on. Matches App.activeArray's signature so api.go can pass that directly.
type Lookup func(id string) (config.Array, bool, error)

func Handler(lookup Lookup) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		arr, ok, err := lookup(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, fmt.Sprintf("unknown array %q", id), http.StatusNotFound)
			return
		}

		scheme := arr.Scheme
		if scheme == "" {
			scheme = "https"
		}
		path := arr.MetricsPath
		if path == "" {
			path = "/metrics"
		}
		url := fmt.Sprintf("%s://%s%s", scheme, arr.Host, path)

		client := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: !arr.VerifyTLS},
			},
		}
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if arr.TokenEnv != "" {
			if token := os.Getenv(arr.TokenEnv); token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf("scrape of %q failed: %v", id, err), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}
