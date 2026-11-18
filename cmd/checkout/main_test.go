package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (transport transportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestCheckoutUpstreamResponses(t *testing.T) {
	upstream := url.URL{Scheme: "http", Host: "shipping", Path: "/api/shipping"}
	for _, tc := range []struct {
		name       string
		body       string
		requestErr error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "valid quote",
			body:       `{"kg":3,"price_cny":12}`,
			wantStatus: http.StatusOK,
			wantBody:   `"total_cny":112`,
		},
		{
			name:       "quote for a different weight",
			body:       `{"kg":4,"price_cny":14}`,
			wantStatus: http.StatusBadGateway,
			wantBody:   "invalid response",
		},
		{
			name:       "quote missing the price",
			body:       `{"kg":3}`,
			wantStatus: http.StatusBadGateway,
			wantBody:   "invalid response",
		},
		{
			name:       "upstream timeout",
			requestErr: context.DeadlineExceeded,
			wantStatus: http.StatusGatewayTimeout,
			wantBody:   "timed out",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet || request.URL.Path != "/api/shipping" || request.URL.RawQuery != "kg=3" {
					t.Errorf("unexpected upstream request: %s %s", request.Method, request.URL)
				}
				if tc.requestErr != nil {
					return nil, tc.requestErr
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}

			handler := checkoutHandler(client, upstream)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/checkout?subtotal_cny=100&kg=3", nil))
			if response.Code != tc.wantStatus || !strings.Contains(response.Body.String(), tc.wantBody) {
				t.Fatalf("response = %d %s, want %d containing %q", response.Code, response.Body.String(), tc.wantStatus, tc.wantBody)
			}
			metrics := httptest.NewRecorder()
			handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			for _, sample := range []string{
				fmt.Sprintf("checkout_http_requests_total{code=\"%d\"} 1\n", tc.wantStatus),
				"checkout_http_request_duration_seconds_count 1\n",
				"checkout_shipping_request_duration_seconds_count 1\n",
			} {
				if !strings.Contains(metrics.Body.String(), sample) {
					t.Errorf("metrics missing sample %q", sample)
				}
			}
		})
	}
}
