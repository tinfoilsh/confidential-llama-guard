package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestParseBackends(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{"two replicas", "http://a:8001,http://b:8001", 2, false},
		{"one replica", "http://a:8001", 1, false},
		{"empty", "", 0, true},
		{"bad url", "not-a-url", 0, true},
		{"missing host", "http://", 0, true},
		{"trailing comma", "http://a:8001,", 1, false},
		{"whitespace", " http://a:8001 , http://b:8001 ", 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bs, err := parseBackends(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(bs) != tc.want {
				t.Fatalf("got %d backends, want %d", len(bs), tc.want)
			}
		})
	}
}

func TestRoundRobin(t *testing.T) {
	var hits []string
	h1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits = append(hits, "h1")
		w.WriteHeader(200)
	}))
	defer h1.Close()
	h2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits = append(hits, "h2")
		w.WriteHeader(200)
	}))
	defer h2.Close()

	bs, err := parseBackends(h1.URL + "," + h2.URL)
	if err != nil {
		t.Fatal(err)
	}

	var counter uint64
	n := uint64(len(bs))
	for range 4 {
		idx := atomic.AddUint64(&counter, 1) % n
		rec := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: 0}
		req := httptest.NewRequest("GET", "/classify", nil)
		bs[idx].proxy.ServeHTTP(rec, req)
	}

	// counter starts at 1 (AddUint64), so first idx=1 (h2), then 0 (h1), etc.
	want := []string{"h2", "h1", "h2", "h1"}
	if len(hits) != len(want) {
		t.Fatalf("got %d hits %v, want %d %v", len(hits), hits, len(want), want)
	}
	for i, h := range hits {
		if h != want[i] {
			t.Fatalf("hit %d: got %s, want %s (all: %v)", i, h, want[i], hits)
		}
	}
}
