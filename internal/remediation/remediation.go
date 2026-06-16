package remediation

import (
	"context"

	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/client"

	srev1alpha1 "github.com/openshift-virtualization/sre-operator/api/v1alpha1"
	"github.com/openshift-virtualization/sre-operator/internal/prometheus"
)

type AlertDispatchResult struct {
	Error          error
	Skipped        bool
	Action         srev1alpha1.RemediationAction
	TriggerName    string
	TargetResource string
}

type Engine struct{}

func NewEngine(c client.Client, t trace.Tracer) *Engine { return &Engine{} }

func (e *Engine) Apply(ctx context.Context, f *srev1alpha1.CheckResult, spec srev1alpha1.CheckSpec) (srev1alpha1.RemediationAction, error) {
	return srev1alpha1.RemediationNone, nil
}

type AlertDispatcher struct{}

func NewAlertDispatcher(e *Engine, t trace.Tracer) *AlertDispatcher { return &AlertDispatcher{} }

func (d *AlertDispatcher) Dispatch(ctx context.Context, results []prometheus.AlertTriggerResult, triggers []srev1alpha1.AlertTrigger, findings []srev1alpha1.CheckResult) []AlertDispatchResult {
	return nil
}
