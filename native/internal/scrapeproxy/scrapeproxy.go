// Package scrapeproxy implements the authenticated fetch of one Pure
// array's native /metrics on Prometheus's behalf, so the per-array bearer
// token lives only here, in config/arrays.yml — never in Prometheus's own
// config, and never over an unauthenticated hop.
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

func Handler(configDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		arr, ok, err := config.GetArray(configDir, id)
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
