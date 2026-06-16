package diagnostics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/client"

	srev1alpha1 "github.com/openshift-virtualization/sre-operator/api/v1alpha1"
)

type Aggregator struct{}

func NewAggregator(c client.Client, t trace.Tracer, version string) *Aggregator {
	return &Aggregator{}
}

type Report struct {
	OTelTraceID string
}

func (a *Aggregator) Build(ctx context.Context, policy *srev1alpha1.SREPolicy, findings []srev1alpha1.CheckResult, count int, d time.Duration) (*Report, error) {
	return &Report{}, nil
}

func (a *Aggregator) PersistToConfigMap(ctx context.Context, r *Report, namespace string) error {
	return nil
}
