package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/trace"

	srev1alpha1 "github.com/openshift-virtualization/sre-operator/api/v1alpha1"
)

type Provider struct{}

func NewProvider(ctx context.Context, spec srev1alpha1.OpenTelemetrySpec) (*Provider, error) {
	return &Provider{}, nil
}

type dummyTracer struct{}
type dummySpan struct{}

func (d *dummySpan) End(options ...trace.SpanEndOption) {}
func (d *dummySpan) AddEvent(name string, options ...trace.EventOption) {}
func (d *dummySpan) IsRecording() bool { return false }
func (d *dummySpan) RecordError(err error, options ...trace.EventOption) {}
func (d *dummySpan) SpanContext() trace.SpanContext { return trace.SpanContext{} }
func (d *dummySpan) SetStatus(code trace.Status, description string) {}
func (d *dummySpan) SetName(name string) {}
func (d *dummySpan) SetAttributes(kv ...trace.KeyValue) {}
func (d *dummySpan) TracerProvider() trace.TracerProvider { return nil }

func (t *dummyTracer) Start(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return ctx, &dummySpan{}
}

func (p *Provider) Tracer() trace.Tracer { return &dummyTracer{} }

func (p *Provider) UpdateStatus(name string, status srev1alpha1.SREPolicyStatus) {}

func (p *Provider) RecordCheckRun(ctx context.Context, name, category string, duration time.Duration, findings int) {}

func (p *Provider) RecordRemediation(ctx context.Context, action, checkName, resourceRef string) {}
