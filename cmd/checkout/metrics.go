package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var metricStatusCodes = [...]int{200, 400, 502, 504}
var latencyBounds = [...]float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 2.5, 5}

type latencyHistogram struct {
	buckets [len(latencyBounds)]uint64
	count   uint64
	sum     float64
}

func (h *latencyHistogram) observe(duration time.Duration) {
	seconds := duration.Seconds()
	h.count++
	h.sum += seconds
	for index, upperBound := range latencyBounds {
		if seconds <= upperBound {
			h.buckets[index]++
		}
	}
}

func (h latencyHistogram) write(w io.Writer, name, description string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", name, description, name)
	for index, upperBound := range latencyBounds {
		fmt.Fprintf(w, "%s_bucket{le=\"%g\"} %d\n", name, upperBound, h.buckets[index])
	}
	fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n%s_sum %.9f\n%s_count %d\n", name, h.count, name, h.sum, name, h.count)
}

type checkoutMetrics struct {
	mu          sync.Mutex
	requests    [len(metricStatusCodes)]uint64
	httpLatency latencyHistogram
	shipLatency latencyHistogram
}

func (m *checkoutMetrics) record(code int, elapsed, shipping time.Duration, shippingCalled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index, status := range metricStatusCodes {
		if status == code {
			m.requests[index]++
			break
		}
	}
	m.httpLatency.observe(elapsed)
	if shippingCalled {
		m.shipLatency.observe(shipping)
	}
}

func (m *checkoutMetrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	requests, httpLatency, shipLatency := m.requests, m.httpLatency, m.shipLatency
	m.mu.Unlock()

	var body strings.Builder
	body.WriteString("# HELP checkout_http_requests_total Completed checkout requests by HTTP status.\n")
	body.WriteString("# TYPE checkout_http_requests_total counter\n")
	for index, status := range metricStatusCodes {
		fmt.Fprintf(&body, "checkout_http_requests_total{code=\"%d\"} %d\n", status, requests[index])
	}
	httpLatency.write(&body, "checkout_http_request_duration_seconds", "Duration of a checkout request in seconds.")
	shipLatency.write(&body, "checkout_shipping_request_duration_seconds", "Duration of a shipping dependency call in seconds.")
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	io.WriteString(w, body.String())
}
