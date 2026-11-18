package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestShippingQuote(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := shippingHandler(logger, "shipping-test", "test", new(atomic.Bool))

	for _, tc := range []struct {
		name, query string
		status      int
		kg, price   int
	}{
		{name: "first kilogram", query: "?kg=1", status: http.StatusOK, kg: 1, price: 8},
		{name: "three kilograms", query: "?kg=3", status: http.StatusOK, kg: 3, price: 12},
		{name: "upper boundary", query: "?kg=20", status: http.StatusOK, kg: 20, price: 46},
		{name: "missing weight", status: http.StatusBadRequest},
		{name: "fractional weight", query: "?kg=1.5", status: http.StatusBadRequest},
		{name: "invalid weight", query: "?kg=invalid", status: http.StatusBadRequest},
		{name: "below range", query: "?kg=0", status: http.StatusBadRequest},
		{name: "above range", query: "?kg=21", status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/shipping"+tc.query, nil))
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d; body = %q", response.Code, tc.status, response.Body.String())
			}
			if tc.status != http.StatusOK {
				return
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			var quote struct {
				Weight  int    `json:"kg"`
				Price   int    `json:"price_cny"`
				Pod     string `json:"pod"`
				Version string `json:"version"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &quote); err != nil {
				t.Fatal(err)
			}
			if quote.Weight != tc.kg || quote.Price != tc.price || quote.Pod != "shipping-test" || quote.Version != version {
				t.Fatalf("unexpected quote: %+v", quote)
			}
		})
	}
}

func TestShippingProbesDuringShutdown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var draining atomic.Bool
	handler := shippingHandler(logger, "shipping-test", "test", &draining)
	for _, tc := range []struct {
		path     string
		draining bool
		status   int
	}{
		{path: "/readyz", status: http.StatusOK},
		{path: "/readyz", draining: true, status: http.StatusServiceUnavailable},
		{path: "/livez", draining: true, status: http.StatusOK},
	} {
		draining.Store(tc.draining)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if response.Code != tc.status {
			t.Errorf("%s (draining=%t) returned %d, want %d", tc.path, tc.draining, response.Code, tc.status)
		}
	}
}
