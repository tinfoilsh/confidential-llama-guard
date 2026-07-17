// Package main implements a round-robin reverse proxy for llama-pg2 replicas.
//
// The shim terminates TLS and forwards plain HTTP to this router on :8080.
// The router round-robins across REPLICA_URLS (comma-separated), giving
// ~2x throughput vs a single replica by eliminating intra-process GIL
// contention (see tf-test/services/cpu-safeguards/writeup_jailbreak_pg2_vs_gliguard.md).
package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
)

type backend struct {
	url   *url.URL
	proxy *httputil.ReverseProxy
}

func main() {
	listenAddr := getenvDefault("LISTEN_ADDR", ":8080")
	rawURLs := os.Getenv("REPLICA_URLS")
	if rawURLs == "" {
		log.Fatal("REPLICA_URLS is required (comma-separated, e.g. http://llama-pg2-1:8001,http://llama-pg2-2:8001)")
	}

	backends, err := parseBackends(rawURLs)
	if err != nil {
		log.Fatalf("parse REPLICA_URLS: %v", err)
	}

	var counter uint64

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","replicas":%d}`, len(backends))
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		n := uint64(len(backends))
		start := atomic.AddUint64(&counter, 1) % n
		var lastErr error
		for i := range n {
			idx := (start + i) % n
			// ReverseProxy ServeHTTP doesn't return an error — it writes to w.
			// We detect failure via a recording ResponseWriter wrapper.
			rec := &statusRecorder{ResponseWriter: w, status: 0}
			backends[idx].proxy.ServeHTTP(rec, r)
			if rec.status != 0 && rec.status < 500 {
				return // success or 4xx (client error) — don't retry
			}
			lastErr = fmt.Errorf("replica %d returned status %d", idx, rec.status)
		}
		if lastErr != nil {
			http.Error(w, "all replicas failed: "+lastErr.Error(), http.StatusBadGateway)
		}
	})

	log.Printf("router listening on %s, %d replicas: %s", listenAddr, len(backends), rawURLs)
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func parseBackends(rawURLs string) ([]*backend, error) {
	var backends []*backend
	for _, raw := range strings.Split(rawURLs, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("bad URL %q: %w", raw, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("bad URL %q: missing scheme or host", raw)
		}
		proxy := httputil.NewSingleHostReverseProxy(u)
		backends = append(backends, &backend{url: u, proxy: proxy})
	}
	if len(backends) == 0 {
		return nil, errors.New("no valid replica URLs")
	}
	return backends, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// statusRecorder wraps http.ResponseWriter to capture the status code,
// so we can detect 5xx errors and retry the next replica.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
