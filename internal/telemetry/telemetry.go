package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	srev1alpha1 "github.com/openshift-virtualization/sre-operator/api/v1alpha1"
)

type Provider struct {
	tracer trace.Tracer
	logger log.Logger
}

func NewProvider(ctx context.Context, spec *srev1alpha1.ObservabilitySpec) (*Provider, error) {
	p := &Provider{
		tracer: &dummyTracer{},
	}

	if spec == nil {
		return p, nil
	}

	// 1. Trace Provider
	if spec.TracingEnabled {
		traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(spec.OTelEndpoint), otlptracegrpc.WithInsecure())
		if err == nil {
			tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(traceExporter))
			otel.SetTracerProvider(tp)
			p.tracer = tp.Tracer("sre-operator")
		}
	}

	// 2. Metric Provider
	if spec.MetricsEnabled {
		metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpoint(spec.OTelEndpoint), otlpmetricgrpc.WithInsecure())
		if err == nil {
			mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)))
			otel.SetMeterProvider(mp)
		}
	}

	// 3. Log Provider
	if spec.LogsEnabled {
		logExporter, err := otlploggrpc.New(ctx, otlploggrpc.WithEndpoint(spec.OTelEndpoint), otlploggrpc.WithInsecure())
		if err == nil {
			lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)))
			global.SetLoggerProvider(lp)
			p.logger = lp.Logger("sre-operator")
		}
	}

	return p, nil
}

func (p *Provider) Tracer() trace.Tracer {
	return p.tracer
}

// LogRecord natively pushes a structured log to the OTLP log exporter
func (p *Provider) LogRecord(ctx context.Context, finding srev1alpha1.CheckResult) {
	if p.logger == nil {
		return
	}
	var record log.Record
	record.SetTimestamp(time.Now())
	record.SetSeverity(log.SeverityInfo)
	if finding.Status == "Failed" {
		record.SetSeverity(log.SeverityError)
	} else if finding.Status == "Degraded" {
		record.SetSeverity(log.SeverityWarn)
	}
	record.SetBody(log.StringValue(finding.Message))
	record.AddAttributes(
		log.String("checkName", finding.CheckName),
		log.String("category", string(finding.Category)),
		log.String("resource", finding.ResourceRef),
		log.String("status", finding.Status),
	)
	p.logger.Emit(ctx, record)
}

// LogSummary natively pushes large textural diagnostics to OTLP
func (p *Provider) LogSummary(ctx context.Context, summary string) {
	if p.logger == nil {
		return
	}
	var record log.Record
	record.SetTimestamp(time.Now())
	record.SetSeverity(log.SeverityInfo)
	record.SetBody(log.StringValue(summary))
	record.AddAttributes(log.String("type", "log-collection-summary"))
	p.logger.Emit(ctx, record)
}

func (p *Provider) UpdateStatus(name string, status srev1alpha1.SREPolicyStatus) {}
func (p *Provider) RecordCheckRun(ctx context.Context, name, category string, duration time.Duration, findings int) {}
func (p *Provider) RecordRemediation(ctx context.Context, action, checkName, resourceRef string) {}

// --- Dummy Tracing Implementation for when Tracing is Disabled ---

type dummyTracer struct{
	trace.Tracer
}
type dummySpan struct{
	trace.Span
}

func (d *dummySpan) End(options ...trace.SpanEndOption) {}
func (d *dummySpan) RecordError(err error, options ...trace.EventOption) {}
func (d *dummySpan) SetAttributes(kv ...attribute.KeyValue) {}
func (d *dummySpan) IsRecording() bool { return false }
func (d *dummySpan) AddEvent(name string, options ...trace.EventOption) {}
func (d *dummySpan) SpanContext() trace.SpanContext { return trace.SpanContext{} }

func (t *dummyTracer) Start(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return ctx, &dummySpan{}
}
