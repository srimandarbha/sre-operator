package prometheus

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	srev1alpha1 "github.com/openshift-virtualization/sre-operator/api/v1alpha1"
)

type AlertTriggerResult struct {
	AlertName string
}

func (a *AlertTriggerResult) ToCheckResult(err error) srev1alpha1.CheckResult {
	return srev1alpha1.CheckResult{
		CheckName:   "alert-" + a.AlertName,
		Category:    srev1alpha1.CheckCategoryPrometheus,
		ResourceRef: "alertmanager",
		Status:      "Degraded",
		Severity:    srev1alpha1.SeverityCritical,
		Message:     "Alert triggered",
	}
}

type Watcher struct{}

func NewWatcher(c *Client, t trace.Tracer) *Watcher { return &Watcher{} }

func (w *Watcher) Evaluate(ctx context.Context, triggers []srev1alpha1.AlertTrigger) ([]AlertTriggerResult, error) {
	return nil, nil
}

type MetricChecker struct{}

func NewMetricChecker(c *Client, t trace.Tracer) *MetricChecker { return &MetricChecker{} }

func (m *MetricChecker) EvaluateAll(ctx context.Context, queries []srev1alpha1.MetricQuery) ([]srev1alpha1.CheckResult, error) {
	return nil, nil
}
