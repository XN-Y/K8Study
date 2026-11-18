package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type shippingQuote struct {
	Weight  int    `json:"kg"`
	Price   *int   `json:"price_cny"`
	Pod     string `json:"pod"`
	Version string `json:"version"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	shippingURL, err := url.ParseRequestURI(os.Getenv("SHIPPING_API_URL"))
	if err != nil || shippingURL.Host == "" || (shippingURL.Scheme != "http" && shippingURL.Scheme != "https") {
		return errors.New("SHIPPING_API_URL must be an absolute HTTP or HTTPS URL")
	}

	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	server := &http.Server{
		Addr:              ":8080",
		Handler:           checkoutHandler(client, *shippingURL),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	served := make(chan error, 1)
	go func() { served <- server.ListenAndServe() }()

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func checkoutHandler(client *http.Client, shippingURL url.URL) http.Handler {
	metrics := new(checkoutMetrics)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /api/checkout", func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		status := http.StatusOK
		var shippingDuration time.Duration
		shippingCalled := false
		defer func() {
			elapsed := time.Since(started)
			metrics.record(status, elapsed, shippingDuration, shippingCalled)
			logger.Info("checkout request", "status", status, "duration_ms", elapsed.Milliseconds(),
				"shipping_called", shippingCalled, "shipping_ms", shippingDuration.Milliseconds())
		}()

		fail := func(code int, message string) {
			status = code
			http.Error(w, message, code)
		}
		weight, weightErr := strconv.Atoi(r.URL.Query().Get("kg"))
		subtotal, subtotalErr := strconv.Atoi(r.URL.Query().Get("subtotal_cny"))
		if weightErr != nil || weight < 1 || weight > 20 ||
			subtotalErr != nil || subtotal < 1 || subtotal > 1_000_000 {
			fail(http.StatusBadRequest, "kg must be 1-20 and subtotal_cny must be 1-1000000")
			return
		}

		shippingStarted := time.Now()
		shippingCalled = true
		quote, err := fetchShippingQuote(r.Context(), client, shippingURL, weight)
		shippingDuration = time.Since(shippingStarted)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				fail(http.StatusGatewayTimeout, "shipping service timed out")
				return
			}
			fail(http.StatusBadGateway, err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(struct {
			Subtotal int           `json:"subtotal_cny"`
			Shipping shippingQuote `json:"shipping"`
			Total    int           `json:"total_cny"`
		}{Subtotal: subtotal, Shipping: quote, Total: subtotal + *quote.Price}); err != nil {
			log.Printf("write checkout response: %v", err)
		}
	})
	return mux
}

func fetchShippingQuote(ctx context.Context, client *http.Client, target url.URL, weight int) (shippingQuote, error) {
	var quote shippingQuote
	query := target.Query()
	query.Set("kg", strconv.Itoa(weight))
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return quote, errors.New("invalid shipping request")
	}
	response, err := client.Do(request)
	if errors.Is(err, context.DeadlineExceeded) {
		return quote, err
	}
	if err != nil {
		return quote, errors.New("shipping service unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return quote, errors.New("shipping service returned an unexpected status")
	}

	const maxQuoteBytes = 4096
	body, err := io.ReadAll(io.LimitReader(response.Body, maxQuoteBytes+1))
	if errors.Is(err, context.DeadlineExceeded) {
		return quote, err
	}
	if err != nil || len(body) > maxQuoteBytes || json.Unmarshal(body, &quote) != nil ||
		quote.Weight != weight || quote.Price == nil || *quote.Price < 0 || *quote.Price > 1_000_000 {
		return shippingQuote{}, errors.New("invalid response from shipping service")
	}
	return quote, nil
}
