// Package prometheus provides a client for polling OpenShift/Thanos Prometheus:
//   - AlertManager API  → firing alerts with labels, annotations, and state
//   - Prometheus HTTP API → raw PromQL instant queries for threshold evaluation
//
// Authentication uses the in-cluster service-account token mounted at
// /var/run/secrets/kubernetes.io/serviceaccount/token, which is how operators
// running inside OpenShift access the cluster-internal Prometheus stack.
package prometheus

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	// DefaultPrometheusURL is the in-cluster Thanos querier used by OpenShift.
	DefaultPrometheusURL = "https://thanos-querier.openshift-monitoring.svc.cluster.local:9091"
	// DefaultAlertManagerURL is the in-cluster AlertManager.
	DefaultAlertManagerURL = "https://alertmanager-main.openshift-monitoring.svc.cluster.local:9094"

	tokenPath   = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	httpTimeout = 15 * time.Second
)

// FiringAlert represents a single alert returned by the AlertManager /api/v2/alerts endpoint.
type FiringAlert struct {
	// Labels is the full label set of the alert (alertname, namespace, severity, etc.)
	Labels map[string]string `json:"labels"`
	// Annotations human-readable fields (summary, description, runbook_url)
	Annotations map[string]string `json:"annotations"`
	// State: active | suppressed | unprocessed
	State string `json:"state"`
	// StartsAt when the alert first fired
	StartsAt time.Time `json:"startsAt"`
	// EndsAt projected end (zero for active alerts)
	EndsAt time.Time `json:"endsAt"`
	// GeneratorURL link back to the Prometheus expression
	GeneratorURL string `json:"generatorURL"`
}

// AlertName returns the alertname label value.
func (a *FiringAlert) AlertName() string { return a.Labels["alertname"] }

// Namespace returns the namespace label if present.
func (a *FiringAlert) Namespace() string { return a.Labels["namespace"] }

// Severity returns the severity label (critical | warning | info).
func (a *FiringAlert) Severity() string { return a.Labels["severity"] }

// QueryResult holds the parsed response from a Prometheus instant query.
type QueryResult struct {
	// Metric labels from the result vector
	Metric map[string]string
	// Value is the scalar float value of the sample
	Value float64
	// Timestamp of the sample
	Timestamp time.Time
}

// PromQueryResponse is the wire-format for /api/v1/query.
type PromQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]interface{}    `json:"value"` // [timestamp, "value_string"]
		} `json:"result"`
	} `json:"data"`
	Error     string `json:"error,omitempty"`
	ErrorType string `json:"errorType,omitempty"`
}

// AlertsResponse is the wire-format for AlertManager /api/v2/alerts.
type AlertsResponse []FiringAlert

// Client is an authenticated HTTP client for Prometheus and AlertManager.
type Client struct {
	prometheusURL   string
	alertManagerURL string
	httpClient      *http.Client
	tracer          trace.Tracer
	token           string // bearer token, refreshed on each call
}

// Config holds connection parameters for the Prometheus client.
type Config struct {
	// PrometheusURL overrides the default in-cluster Thanos querier URL
	PrometheusURL string
	// AlertManagerURL overrides the default in-cluster AlertManager URL
	AlertManagerURL string
	// InsecureSkipVerify disables TLS verification (dev/test only)
	InsecureSkipVerify bool
	// BearerTokenSecretRef if non-empty, reads the bearer token from this path
	// instead of the default service-account token
	BearerTokenPath string
}

// NewClient constructs a Prometheus client using in-cluster service-account auth.
func NewClient(cfg Config, tracer trace.Tracer) *Client {
	promURL := cfg.PrometheusURL
	if promURL == "" {
		promURL = DefaultPrometheusURL
	}
	amURL := cfg.AlertManagerURL
	if amURL == "" {
		amURL = DefaultAlertManagerURL
	}
	tokenPath_ := tokenPath
	if cfg.BearerTokenPath != "" {
		tokenPath_ = cfg.BearerTokenPath
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // controlled by operator config
		},
		// Connection pool tuning: the operator is the only caller of this
		// transport, polling two endpoints (AlertManager + Thanos) every
		// intervalSeconds. Two idle conns per host is sufficient.
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
	}

	token, _ := readToken(tokenPath_)

	return &Client{
		prometheusURL:   promURL,
		alertManagerURL: amURL,
		httpClient:      &http.Client{Timeout: httpTimeout, Transport: transport},
		tracer:          tracer,
		token:           token,
	}
}

// FiringAlerts returns all currently active alerts from AlertManager,
// optionally filtered by label matchers (e.g. {"severity": "critical"}).
func (c *Client) FiringAlerts(ctx context.Context, labelFilters map[string]string) ([]FiringAlert, error) {
	ctx, span := c.tracer.Start(ctx, "Prometheus.FiringAlerts",
		trace.WithAttributes(attribute.Int("filters.count", len(labelFilters))),
	)
	defer span.End()

	endpoint := c.alertManagerURL + "/api/v2/alerts"

	// Build filter query params: ?filter=alertname="KubeVirtVMIPhaseTransitionTimeout"
	if len(labelFilters) > 0 {
		var filters []string
		for k, v := range labelFilters {
			filters = append(filters, fmt.Sprintf(`%s="%s"`, k, v))
		}
		endpoint += "?filter=" + url.QueryEscape("{"+strings.Join(filters, ",")+"}")
	}

	body, err := c.get(ctx, endpoint)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("alertmanager GET: %w", err)
	}

	var alerts AlertsResponse
	if err := json.Unmarshal(body, &alerts); err != nil {
		return nil, fmt.Errorf("decode AlertManager response: %w\nbody: %.500s", err, body)
	}

	// Filter to active-only (suppress silenced/inhibited)
	var active []FiringAlert
	for _, a := range alerts {
		if a.State == "active" {
			active = append(active, a)
		}
	}

	span.SetAttributes(
		attribute.Int("alerts.total", len(alerts)),
		attribute.Int("alerts.active", len(active)),
	)
	return active, nil
}

// AlertsByName returns active alerts matching a specific alertname.
func (c *Client) AlertsByName(ctx context.Context, alertName string) ([]FiringAlert, error) {
	return c.FiringAlerts(ctx, map[string]string{"alertname": alertName})
}

// AlertsMatchingNames returns active alerts whose alertname is in the provided set.
func (c *Client) AlertsMatchingNames(ctx context.Context, names []string) ([]FiringAlert, error) {
	all, err := c.FiringAlerts(ctx, nil)
	if err != nil {
		return nil, err
	}
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	var matched []FiringAlert
	for _, a := range all {
		if nameSet[a.AlertName()] {
			matched = append(matched, a)
		}
	}
	return matched, nil
}

// QueryInstant executes a PromQL instant query and returns the result vector.
func (c *Client) QueryInstant(ctx context.Context, query string) ([]QueryResult, error) {
	ctx, span := c.tracer.Start(ctx, "Prometheus.QueryInstant",
		trace.WithAttributes(attribute.String("query", query)),
	)
	defer span.End()

	endpoint := c.prometheusURL + "/api/v1/query?query=" + url.QueryEscape(query)

	body, err := c.get(ctx, endpoint)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("prometheus query GET: %w", err)
	}

	var resp PromQueryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode Prometheus response: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prometheus query error [%s]: %s", resp.ErrorType, resp.Error)
	}

	var results []QueryResult
	for _, r := range resp.Data.Result {
		var val float64
		var ts time.Time

		if len(r.Value) == 2 {
			// Value[0] is float64 unix timestamp, Value[1] is string value
			if tsFloat, ok := r.Value[0].(float64); ok {
				ts = time.Unix(int64(tsFloat), 0)
			}
			if valStr, ok := r.Value[1].(string); ok {
				fmt.Sscanf(valStr, "%f", &val) //nolint:errcheck
			}
		}

		results = append(results, QueryResult{
			Metric:    r.Metric,
			Value:     val,
			Timestamp: ts,
		})
	}

	span.SetAttributes(attribute.Int("results.count", len(results)))
	return results, nil
}

// QueryScalar is a convenience wrapper that returns the first float64 result value.
// Returns (0, false) if no results or the query returns an error.
func (c *Client) QueryScalar(ctx context.Context, query string) (float64, bool, error) {
	results, err := c.QueryInstant(ctx, query)
	if err != nil {
		return 0, false, err
	}
	if len(results) == 0 {
		return 0, false, nil
	}
	return results[0].Value, true, nil
}

// get performs an authenticated HTTP GET and returns the response body.
func (c *Client) get(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	// Refresh token on each call — the mounted token is rotated automatically
	if token, err := readToken(tokenPath); err == nil && token != "" {
		c.token = token
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s: %.500s", resp.StatusCode, endpoint, body)
	}

	return body, nil
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
