package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) == 2 && os.Args[1] == "report" {
		logger.Info("demo shipping report generated", "version", version, "sample_orders", 3)
		return
	}

	pod := os.Getenv("POD_NAME")
	if pod == "" {
		pod, _ = os.Hostname()
	}
	greeting := os.Getenv("GREETING")
	if greeting == "" {
		greeting = "Hello from Kubernetes"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var draining atomic.Bool

	server := &http.Server{
		Addr:              ":8080",
		Handler:           shippingHandler(logger, pod, greeting, &draining),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	shutdown := make(chan error, 1)
	go func() {
		<-ctx.Done()
		draining.Store(true)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdown <- server.Shutdown(shutdownCtx)
	}()

	logger.Info("HTTP server starting", "address", server.Addr, "pod", pod, "version", version)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
	if err := <-shutdown; err != nil {
		logger.Error("HTTP shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("HTTP server stopped", "pod", pod)
}

func shippingHandler(logger *slog.Logger, pod, greeting string, draining *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if draining.Load() {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"greeting": greeting, "pod": pod, "version": version})
	})
	mux.HandleFunc("GET /api/shipping", func(w http.ResponseWriter, r *http.Request) {
		weight, err := strconv.Atoi(r.URL.Query().Get("kg"))
		if err != nil || weight < 1 || weight > 20 {
			http.Error(w, "kg must be an integer between 1 and 20", http.StatusBadRequest)
			return
		}
		price := 8 + (weight-1)*2
		logger.Info("shipping quote", "kg", weight, "price_cny", price, "pod", pod)
		writeJSON(w, struct {
			Weight  int    `json:"kg"`
			Price   int    `json:"price_cny"`
			Pod     string `json:"pod"`
			Version string `json:"version"`
		}{Weight: weight, Price: price, Pod: pod, Version: version})
	})

	return mux
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("encode response", "error", err)
	}
}
