// Package controllers implements the SREPolicy reconciler.
package controllers

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	srev1alpha1 "github.com/openshift-virtualization/sre-operator/api/v1alpha1"
	"github.com/openshift-virtualization/sre-operator/internal/checks"
	"github.com/openshift-virtualization/sre-operator/internal/diagnostics"
	"github.com/openshift-virtualization/sre-operator/internal/prometheus"
	"github.com/openshift-virtualization/sre-operator/internal/remediation"
	"github.com/openshift-virtualization/sre-operator/internal/telemetry"
)

// OperatorVersion is injected at build time via -ldflags.
var OperatorVersion = "0.1.0"

// SREPolicyReconciler reconciles SREPolicy objects.
//
// +kubebuilder:rbac:groups=sre.kubevirt.io,resources=srepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sre.kubevirt.io,resources=srepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sre.kubevirt.io,resources=srepolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods;nodes;persistentvolumeclaims;persistentvolumes;services;endpoints;events;configmaps;namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=delete
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=nodes,verbs=patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=create;update;patch
// +kubebuilder:rbac:groups=policy,resources=podevictions,verbs=create
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances;virtualmachineinstancemigrations,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators;clusterversions,verbs=get;list;watch
// +kubebuilder:rbac:groups=machineconfiguration.openshift.io,resources=machineconfigpools,verbs=get;list;watch
type SREPolicyReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Recorder   record.EventRecorder
	Telemetry  *telemetry.Provider
	KubeClient kubernetes.Interface // for pod log streaming

	// promClient is reused across reconcile cycles to preserve the HTTP
	// connection pool. Rebuilt only when prometheus spec changes.
	promClient     *prometheus.Client
	lastPromConfig string // JSON-serialised PrometheusSpec for change detection

	// lastOTelEndpoint tracks the endpoint the current Telemetry provider
	// was initialised with so we only re-dial when the spec actually changes.
	lastOTelEndpoint string
}

const (
	conditionTypeReady       = "Ready"
	conditionTypeOTelHealthy = "OTelHealthy"
	conditionTypePromHealthy = "PrometheusHealthy"
	conditionTypeOCPHealthy  = "OCPClusterHealthy"
	defaultRequeueAfter      = 60 * time.Second
	finalizerName            = "sre.kubevirt.io/finalizer"
	logDiagnosticsConfigMap  = "sre-log-diagnostics"
)

// Reconcile is the main control loop entry point.
func (r *SREPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("srepolicy", req.NamespacedName)

	policy := &srev1alpha1.SREPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get SREPolicy: %w", err)
	}

	if !policy.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, policy)
	}

	if !containsString(policy.Finalizers, finalizerName) {
		policy.Finalizers = append(policy.Finalizers, finalizerName)
		if err := r.Update(ctx, policy); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if policy.Spec.Paused {
		logger.Info("SREPolicy is paused — skipping check cycle")
		return r.updatePhase(ctx, policy, "Paused", nil)
	}

	tracer := r.Telemetry.Tracer()
	ctx, span := tracer.Start(ctx, "SREPolicyReconciler.Reconcile",
		trace.WithAttributes(
			attribute.String("policy.name", policy.Name),
			attribute.String("policy.namespace", policy.Namespace),
		),
	)
	defer span.End()

	// Re-init OTel only when the endpoint changes, not every cycle.
	// The original code called NewProvider (which dials a new gRPC connection)
	// on every reconcile, leaking the old connection and its goroutines.
	if policy.Spec.OpenTelemetry != nil && policy.Spec.OpenTelemetry.Enabled {
		if policy.Spec.OpenTelemetry.Endpoint != r.lastOTelEndpoint {
			if err := r.reconcileOTel(ctx, policy); err != nil {
				logger.Error(err, "OTel init failed — continuing without telemetry export")
				setCondition(&policy.Status, conditionTypeOTelHealthy, metav1.ConditionFalse,
					"OTelInitFailed", err.Error())
			} else {
				r.lastOTelEndpoint = policy.Spec.OpenTelemetry.Endpoint
				setCondition(&policy.Status, conditionTypeOTelHealthy, metav1.ConditionTrue,
					"OTelConnected", "OTel endpoint reachable")
				reachable := true
				policy.Status.OtelEndpointReachable = &reachable
			}
		}
	}

	namespaces := policy.Spec.TargetNamespaces
	if len(namespaces) == 0 {
		namespaces = []string{""}
	}

	scanStart := time.Now()

	// ── 1. Built-in checks (VM, Node, Storage, Network) ──────────────────────
	allFindings, remediationCount, err := r.runBuiltInChecks(ctx, policy, namespaces, tracer)
	if err != nil {
		span.RecordError(err)
		return r.updatePhase(ctx, policy, "Error", err)
	}

	// ── 2. OCP Cluster checks (CO, CV, MCP) ──────────────────────────────────
	ocpFindings, ocpHealthy := r.runOCPClusterChecks(ctx, policy, tracer)
	allFindings = append(allFindings, ocpFindings...)
	policy.Status.OCPClusterHealthy = &ocpHealthy
	if !ocpHealthy {
		setCondition(&policy.Status, conditionTypeOCPHealthy, metav1.ConditionFalse,
			"OCPClusterDegraded", fmt.Sprintf("%d OCP cluster findings", len(ocpFindings)))
	} else {
		setCondition(&policy.Status, conditionTypeOCPHealthy, metav1.ConditionTrue,
			"OCPClusterHealthy", "All ClusterOperators, ClusterVersion, and MCPs healthy")
	}

	// ── 3. Prometheus alert triggers + raw metric queries ────────────────────
	promFindings, alertTriggerResults, alertDispatchResults := r.runPrometheusChecks(ctx, policy, tracer)
	allFindings = append(allFindings, promFindings...)

	// Update alert status fields
	firingNames := make([]string, 0)
	for _, tr := range alertTriggerResults {
		firingNames = append(firingNames, tr.AlertName)
	}
	policy.Status.FiringAlertCount = int32(len(alertTriggerResults))
	policy.Status.FiringAlertNames = firingNames

	// Count alert-triggered remediations
	for _, dr := range alertDispatchResults {
		if !dr.Skipped && dr.Error == nil &&
			dr.Action != srev1alpha1.RemediationAlert &&
			dr.Action != srev1alpha1.RemediationNone &&
			dr.Action != srev1alpha1.RemediationDiagnose {
			remediationCount++
		}
	}

	// ── 4. Log collection ─────────────────────────────────────────────────────
	logFindings := r.runLogCollection(ctx, policy, alertTriggerResults, tracer)
	allFindings = append(allFindings, logFindings...)

	scanDuration := time.Since(scanStart)

	// ── 5. Diagnostics report ─────────────────────────────────────────────────
	diagAgg := diagnostics.NewAggregator(r.Client, tracer, OperatorVersion)
	report, diagErr := diagAgg.Build(ctx, policy, allFindings, remediationCount, scanDuration)
	if diagErr != nil {
		logger.Error(diagErr, "Diagnostic report build failed — non-fatal")
	} else {
		if cmErr := diagAgg.PersistToConfigMap(ctx, report, policy.Namespace); cmErr != nil {
			logger.V(1).Info("Diagnostic ConfigMap persist failed", "err", cmErr)
		}
		if report.OTelTraceID != "" {
			for i := range allFindings {
				allFindings[i].TraceID = report.OTelTraceID
			}
		}
	}

	// Cap findings written to status to prevent the SREPolicy object growing
	// beyond etcd's 1.5MB limit. The full set is always in the DiagnosticReport
	// ConfigMap. Keep the most severe findings first.
	const maxStatusFindings = 500
	statusFindings := allFindings
	if len(allFindings) > maxStatusFindings {
		// Sort: Failed > Degraded > Healthy, Critical > Warning > Info
		sortFindingsByPriority(allFindings)
		statusFindings = allFindings[:maxStatusFindings]
		logger.Info("Findings capped in status",
			"total", len(allFindings), "cap", maxStatusFindings)
	}

	now := metav1.Now()
	minInterval := r.minimumInterval(policy.Spec.Checks)
	next := metav1.NewTime(now.Add(time.Duration(minInterval) * time.Second))

	policy.Status.Findings = statusFindings
	policy.Status.LastScanTime = &now
	policy.Status.NextScanTime = &next
	policy.Status.TotalChecks = int32(len(allFindings))
	policy.Status.RemediationsApplied = int32(remediationCount)
	policy.Status.ObservedGeneration = policy.Generation

	healthy, degraded, failed := countBySeverity(allFindings)
	policy.Status.HealthyCount = int32(healthy)
	policy.Status.DegradedCount = int32(degraded)
	policy.Status.FailedCount = int32(failed)

	phase := "Active"
	if failed > 0 {
		phase = "Degraded"
	}

	setCondition(&policy.Status, conditionTypeReady, metav1.ConditionTrue,
		"CheckCycleComplete",
		fmt.Sprintf("Cycle: %d healthy, %d degraded, %d failed | alerts: %d | scan: %.1fs",
			healthy, degraded, failed, len(alertTriggerResults), scanDuration.Seconds()),
	)

	if policy.Spec.Notifications == nil || policy.Spec.Notifications.EventsEnabled {
		r.emitEvents(policy, allFindings)
	}

	r.Telemetry.UpdateStatus(fmt.Sprintf("%s/%s", policy.Namespace, policy.Name), policy.Status)

	span.SetAttributes(
		attribute.Int("findings.healthy", healthy),
		attribute.Int("findings.degraded", degraded),
		attribute.Int("findings.failed", failed),
		attribute.Int("remediations.applied", remediationCount),
		attribute.Int("alerts.fired", len(alertTriggerResults)),
		attribute.Float64("scan.duration_seconds", scanDuration.Seconds()),
	)

	result, finalErr := r.updatePhase(ctx, policy, phase, nil)
	result.RequeueAfter = time.Duration(minInterval) * time.Second
	return result, finalErr
}

// ── Built-in checks ───────────────────────────────────────────────────────────

func (r *SREPolicyReconciler) runBuiltInChecks(
	ctx context.Context,
	policy *srev1alpha1.SREPolicy,
	namespaces []string,
	tracer trace.Tracer,
) ([]srev1alpha1.CheckResult, int, error) {
	logger := log.FromContext(ctx)
	remEngine := remediation.NewEngine(r.Client, tracer)

	var allFindings []srev1alpha1.CheckResult
	totalRemediations := 0

	for _, checkSpec := range policy.Spec.Checks {
		if !checkSpec.Enabled {
			continue
		}
		for _, ns := range namespaces {
			registry := r.buildRegistry(ns, tracer)
			checkerList := registry.For(checkSpec.Category)

			for _, checker := range checkerList {
				start := time.Now()
				checkCtx, checkSpan := tracer.Start(ctx,
					fmt.Sprintf("check.%s.%s", checkSpec.Category, checker.Name()),
					trace.WithAttributes(
						attribute.String("check.spec.name", checkSpec.Name),
						attribute.String("namespace", ns),
					),
				)
				findings, err := checker.Run(checkCtx, checkSpec)
				duration := time.Since(start)
				checkSpan.End()

				if err != nil {
					logger.Error(err, "Check failed", "checker", checker.Name(), "namespace", ns)
					continue
				}

				r.Telemetry.RecordCheckRun(ctx, checkSpec.Name, string(checkSpec.Category), duration, len(findings))

				for i := range findings {
					f := &findings[i]
					if f.Status == "Healthy" || checkSpec.Remediation == srev1alpha1.RemediationNone {
						continue
					}
					action, remErr := remEngine.Apply(ctx, f, checkSpec)
					if remErr != nil {
						logger.Error(remErr, "Remediation failed", "resource", f.ResourceRef)
					} else if action != srev1alpha1.RemediationNone && action != srev1alpha1.RemediationAlert {
						f.RemediationApplied = action
						f.RemediationAttempts++
						totalRemediations++
						r.Telemetry.RecordRemediation(ctx, string(action), checkSpec.Name, f.ResourceRef)
					}
				}
				allFindings = append(allFindings, findings...)
			}
		}
	}
	return allFindings, totalRemediations, nil
}

// ── OCP Cluster checks ────────────────────────────────────────────────────────

func (r *SREPolicyReconciler) runOCPClusterChecks(
	ctx context.Context,
	policy *srev1alpha1.SREPolicy,
	tracer trace.Tracer,
) ([]srev1alpha1.CheckResult, bool) {
	if policy.Spec.OCPCluster == nil || !policy.Spec.OCPCluster.Enabled {
		return nil, true
	}

	logger := log.FromContext(ctx)
	spec := policy.Spec.OCPCluster

	checker := checks.NewOCPClusterChecker(r.Client, tracer)

	// Build a synthetic CheckSpec to pass to the checker
	checkSpec := srev1alpha1.CheckSpec{
		Name:        "ocp-cluster",
		Category:    srev1alpha1.CheckCategoryOCPCluster,
		Enabled:     true,
		Severity:    srev1alpha1.SeverityCritical,
		Remediation: spec.Remediation,
		RunbookURL:  spec.RunbookURL,
	}

	findings, err := checker.Run(ctx, checkSpec)
	if err != nil {
		logger.Error(err, "OCP cluster check failed")
		return nil, true // treat failure as non-fatal, don't block the cycle
	}

	healthy := true
	for _, f := range findings {
		if f.Status != "Healthy" {
			healthy = false
			break
		}
	}
	return findings, healthy
}

// ── Prometheus checks (alerts + metric queries) ───────────────────────────────

func (r *SREPolicyReconciler) runPrometheusChecks(
	ctx context.Context,
	policy *srev1alpha1.SREPolicy,
	tracer trace.Tracer,
) ([]srev1alpha1.CheckResult, []prometheus.AlertTriggerResult, []remediation.AlertDispatchResult) {
	if policy.Spec.Prometheus == nil || !policy.Spec.Prometheus.Enabled {
		return nil, nil, nil
	}

	logger := log.FromContext(ctx)
	promSpec := policy.Spec.Prometheus

	ctx, span := tracer.Start(ctx, "PrometheusChecks.Run")
	defer span.End()

	// Reuse the HTTP client across reconcile cycles to preserve the connection
	// pool. Only rebuild when the spec (URL / TLS / token) changes.
	// The original code called NewClient every reconcile, discarding the pool.
	specKey := fmt.Sprintf("%s|%s|%v", promSpec.PrometheusURL, promSpec.AlertManagerURL, promSpec.InsecureSkipVerify)
	if r.promClient == nil || r.lastPromConfig != specKey {
		promCfg := prometheus.Config{
			PrometheusURL:      promSpec.PrometheusURL,
			AlertManagerURL:    promSpec.AlertManagerURL,
			InsecureSkipVerify: promSpec.InsecureSkipVerify,
		}
		if promSpec.BearerTokenSecretRef != nil {
			promCfg.BearerTokenPath = fmt.Sprintf(
				"/var/run/secrets/prometheus/%s/%s",
				promSpec.BearerTokenSecretRef.Name,
				promSpec.BearerTokenSecretRef.Key,
			)
		}
		r.promClient = prometheus.NewClient(promCfg, tracer)
		r.lastPromConfig = specKey
	}
	promClient := r.promClient

	var allFindings []srev1alpha1.CheckResult

	// ── Alert triggers ────────────────────────────────────────────────────────
	var alertTriggerResults []prometheus.AlertTriggerResult
	var alertDispatchResults []remediation.AlertDispatchResult

	if len(promSpec.AlertTriggers) > 0 {
		watcher := prometheus.NewWatcher(promClient, tracer)
		triggerResults, err := watcher.Evaluate(ctx, promSpec.AlertTriggers)
		if err != nil {
			logger.Error(err, "AlertManager polling failed")
			setCondition(&policy.Status, conditionTypePromHealthy, metav1.ConditionFalse,
				"AlertManagerUnreachable", err.Error())
		} else {
			setCondition(&policy.Status, conditionTypePromHealthy, metav1.ConditionTrue,
				"AlertManagerReachable", fmt.Sprintf("%d triggers evaluated, %d fired", len(promSpec.AlertTriggers), len(triggerResults)))

			alertTriggerResults = triggerResults
			span.SetAttributes(attribute.Int("alert_triggers.fired", len(triggerResults)))

			// Convert trigger results → CheckResults for the findings pipeline
			now := metav1.Now()
			_ = now
			for i := range triggerResults {
				finding := triggerResults[i].ToCheckResult(nil)
				allFindings = append(allFindings, finding)
			}

			// Dispatch remediations for fired triggers
			engine := remediation.NewEngine(r.Client, tracer)
			dispatcher := remediation.NewAlertDispatcher(engine, tracer)
			alertDispatchResults = dispatcher.Dispatch(ctx, triggerResults, promSpec.AlertTriggers, policy.Status.Findings)

			for _, dr := range alertDispatchResults {
				if dr.Error != nil {
					logger.Error(dr.Error, "Alert-triggered remediation failed",
						"trigger", dr.TriggerName, "target", dr.TargetResource)
				} else if !dr.Skipped {
					logger.Info("Alert-triggered remediation dispatched",
						"trigger", dr.TriggerName,
						"action", dr.Action,
						"target", dr.TargetResource,
					)
				}
			}
		}
	}

	// ── Raw metric queries ────────────────────────────────────────────────────
	if len(promSpec.MetricQueries) > 0 {
		metricChecker := prometheus.NewMetricChecker(promClient, tracer)
		metricFindings, err := metricChecker.EvaluateAll(ctx, promSpec.MetricQueries)
		if err != nil {
			logger.Error(err, "Metric query evaluation failed")
		} else {
			allFindings = append(allFindings, metricFindings...)
			span.SetAttributes(attribute.Int("metric_queries.findings", len(metricFindings)))
		}
	}

	return allFindings, alertTriggerResults, alertDispatchResults
}

// ── Log collection ────────────────────────────────────────────────────────────

func (r *SREPolicyReconciler) runLogCollection(
	ctx context.Context,
	policy *srev1alpha1.SREPolicy,
	firedTriggers []prometheus.AlertTriggerResult,
	tracer trace.Tracer,
) []srev1alpha1.CheckResult {
	if policy.Spec.LogCollection == nil || !policy.Spec.LogCollection.Enabled {
		return nil
	}
	if r.KubeClient == nil {
		return nil
	}

	spec := *policy.Spec.LogCollection
	logger := log.FromContext(ctx)

	// Honour OnAlertOnly: skip log collection if no alerts fired this cycle
	if spec.OnAlertOnly && len(firedTriggers) == 0 {
		// Also skip if no alert triggers are configured at all
		if policy.Spec.Prometheus == nil || len(policy.Spec.Prometheus.AlertTriggers) == 0 {
			return nil
		}
		logger.V(1).Info("OnAlertOnly=true and no alerts fired — skipping log collection")
		return nil
	}

	collector := diagnostics.NewLogCollector(r.Client, r.KubeClient, tracer)
	collection, err := collector.Collect(ctx, spec, policy.Spec.TargetNamespaces)
	if err != nil {
		logger.Error(err, "Log collection failed")
		return nil
	}

	// Persist log summary to its own ConfigMap
	if spec.PersistToConfigMap {
		summary := collection.FormatReport()
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      logDiagnosticsConfigMap,
				Namespace: policy.Namespace,
				Labels:    map[string]string{"sre.kubevirt.io/component": "log-diagnostics"},
			},
			Data: map[string]string{
				"log-summary.txt": summary,
				"last-updated":    collection.CollectedAt.Format(time.RFC3339),
			},
		}
		existing := &corev1.ConfigMap{}
		if getErr := r.Get(ctx, client.ObjectKey{Name: cm.Name, Namespace: cm.Namespace}, existing); getErr != nil {
			if createErr := r.Create(ctx, cm); createErr != nil {
				logger.V(1).Info("Failed to create log-diagnostics ConfigMap", "err", createErr)
			}
		} else {
			patch := client.MergeFrom(existing.DeepCopy())
			existing.Data = cm.Data
			if patchErr := r.Patch(ctx, existing, patch); patchErr != nil {
				logger.V(1).Info("Failed to patch log-diagnostics ConfigMap", "err", patchErr)
			}
		}
		policy.Status.LogDiagnosticsConfigMap = fmt.Sprintf("%s/%s", policy.Namespace, logDiagnosticsConfigMap)
	}

	return collection.ToCheckResults("log-collection")
}

// ── Registry ──────────────────────────────────────────────────────────────────

func (r *SREPolicyReconciler) buildRegistry(namespace string, tracer trace.Tracer) *checks.Registry {
	reg := checks.NewRegistry()
	reg.Register(checks.NewVMHealthChecker(r.Client, tracer, namespace))
	reg.Register(checks.NewNodeHealthChecker(r.Client, tracer))
	reg.Register(checks.NewStorageChecker(r.Client, tracer, namespace))
	reg.Register(checks.NewNetworkChecker(r.Client, tracer, namespace))
	// OCPClusterChecker is run separately (cluster-scoped, not per-namespace)
	return reg
}

// ── OTel ──────────────────────────────────────────────────────────────────────

func (r *SREPolicyReconciler) reconcileOTel(ctx context.Context, policy *srev1alpha1.SREPolicy) error {
	if policy.Spec.OpenTelemetry == nil {
		return nil
	}
	provider, err := telemetry.NewProvider(ctx, *policy.Spec.OpenTelemetry)
	if err != nil {
		return err
	}
	r.Telemetry = provider
	return nil
}

// ── Events ────────────────────────────────────────────────────────────────────

func (r *SREPolicyReconciler) emitEvents(policy *srev1alpha1.SREPolicy, findings []srev1alpha1.CheckResult) {
	for _, f := range findings {
		if f.Status == "Healthy" {
			continue
		}
		eventType := corev1.EventTypeNormal
		if f.Severity == srev1alpha1.SeverityCritical {
			eventType = corev1.EventTypeWarning
		}
		msg := fmt.Sprintf("[%s] %s: %s", f.Severity, f.ResourceRef, f.Message)
		if f.RunbookURL != "" {
			msg += " | Runbook: " + f.RunbookURL
		}
		if f.TraceID != "" {
			msg += " | TraceID: " + f.TraceID
		}
		r.Recorder.Eventf(policy, eventType,
			fmt.Sprintf("SRE%s%s", f.Category, f.Status), msg)
	}
}

// ── Deletion ──────────────────────────────────────────────────────────────────

func (r *SREPolicyReconciler) handleDeletion(ctx context.Context, policy *srev1alpha1.SREPolicy) (ctrl.Result, error) {
	if containsString(policy.Finalizers, finalizerName) {
		policy.Finalizers = removeString(policy.Finalizers, finalizerName)
		if err := r.Update(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// ── Status ────────────────────────────────────────────────────────────────────

func (r *SREPolicyReconciler) updatePhase(ctx context.Context, policy *srev1alpha1.SREPolicy, phase string, reconcileErr error) (ctrl.Result, error) {
	policy.Status.Phase = phase
	if err := r.Status().Update(ctx, policy); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}
	if reconcileErr != nil {
		return ctrl.Result{RequeueAfter: defaultRequeueAfter}, reconcileErr
	}
	return ctrl.Result{RequeueAfter: defaultRequeueAfter}, nil
}

func (r *SREPolicyReconciler) minimumInterval(specs []srev1alpha1.CheckSpec) int32 {
	min := int32(60)
	for _, s := range specs {
		if s.IntervalSeconds > 0 && s.IntervalSeconds < min {
			min = s.IntervalSeconds
		}
	}
	if min < 30 {
		min = 30
	}
	return min
}

// SetupWithManager registers the controller with the manager.
func (r *SREPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&srev1alpha1.SREPolicy{}).
		Complete(r)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func setCondition(status *srev1alpha1.SREPolicyStatus, condType string, condStatus metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range status.Conditions {
		if c.Type == condType {
			status.Conditions[i].Status = condStatus
			status.Conditions[i].Reason = reason
			status.Conditions[i].Message = message
			status.Conditions[i].LastTransitionTime = now
			return
		}
	}
	status.Conditions = append(status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             condStatus,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

func countBySeverity(findings []srev1alpha1.CheckResult) (healthy, degraded, failed int) {
	seen := make(map[string]bool)
	for _, f := range findings {
		if seen[f.ResourceRef] {
			continue
		}
		seen[f.ResourceRef] = true
		switch f.Status {
		case "Healthy":
			healthy++
		case "Degraded":
			degraded++
		case "Failed":
			failed++
		}
	}
	return
}

// sortFindingsByPriority sorts findings Failed > Degraded > Healthy,
// and within each status Critical > Warning > Info.
// Used to ensure the most important findings survive the status cap.
func sortFindingsByPriority(findings []srev1alpha1.CheckResult) {
	statusRank := map[string]int{"Failed": 0, "Degraded": 1, "Unknown": 2, "Healthy": 3}
	severityRank := map[srev1alpha1.SeverityLevel]int{
		srev1alpha1.SeverityCritical: 0,
		srev1alpha1.SeverityWarning:  1,
		srev1alpha1.SeverityInfo:     2,
	}
	for i := 1; i < len(findings); i++ {
		for j := i; j > 0; j-- {
			a, b := findings[j-1], findings[j]
			ar := statusRank[a.Status]*10 + severityRank[a.Severity]
			br := statusRank[b.Status]*10 + severityRank[b.Severity]
			if ar > br {
				findings[j-1], findings[j] = findings[j], findings[j-1]
			} else {
				break
			}
		}
	}
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	var out []string
	for _, v := range slice {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}
