// logcollector.go — collects and analyses logs from KubeVirt pods and
// OpenShift CNV operator pods for inclusion in DiagnosticReports.
//
// Log collection uses the Kubernetes API (pod/log subresource) and is
// intentionally bounded: only the last N lines are fetched, and only
// error-bearing lines are retained for the report to keep payloads small.
package diagnostics

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	srev1alpha1 "github.com/openshift-virtualization/sre-operator/api/v1alpha1"
)

const (
	// maxLogLines is the maximum number of tail lines fetched per container.
	maxLogLines = 200
	// maxLogLinesPerContainer kept in the report (error lines only).
	maxErrLinesPerContainer = 50
	// logCollectTimeout per container.
	logCollectTimeout = 10 * time.Second
)

// errorPatterns are compiled regexps that flag a log line as significant.
var errorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(error|err|fatal|panic|exception|oom|kill|failed|failure|crash)\b`),
	regexp.MustCompile(`(?i)reconcil.*fail`),
	regexp.MustCompile(`(?i)unable\s+to`),
	regexp.MustCompile(`(?i)permission\s+denied`),
	regexp.MustCompile(`(?i)connection\s+(refused|reset|timeout)`),
	regexp.MustCompile(`(?i)ImagePullBackOff|ErrImagePull`),
	regexp.MustCompile(`(?i)OOMKilled`),
}

// PodLogEntry is a single filtered log line with pod/container metadata.
type PodLogEntry struct {
	Namespace     string    `json:"namespace"`
	PodName       string    `json:"podName"`
	ContainerName string    `json:"containerName"`
	Line          string    `json:"line"`
	CollectedAt   time.Time `json:"collectedAt"`
}

// LogCollection is the aggregated log output for one diagnostic cycle.
type LogCollection struct {
	// VMPodLogs are filtered error lines from virt-launcher and virt-handler pods
	VMPodLogs []PodLogEntry `json:"vmPodLogs,omitempty"`
	// OperatorLogs are filtered error lines from openshift-cnv operator pods
	OperatorLogs []PodLogEntry `json:"operatorLogs,omitempty"`
	// CollectedAt timestamp
	CollectedAt time.Time `json:"collectedAt"`
	// Errors lists any pods where log collection itself failed
	Errors []string `json:"errors,omitempty"`
}

// LogCollector fetches and filters pod logs for diagnostic purposes.
type LogCollector struct {
	// client is used to list pods
	client client.Client
	// kubeClient is used to stream pod logs (controller-runtime client doesn't expose pod/log)
	kubeClient kubernetes.Interface
	tracer     trace.Tracer
}

// NewLogCollector constructs a LogCollector.
// kubeClient can be built from the rest.Config via kubernetes.NewForConfig(mgr.GetConfig()).
func NewLogCollector(c client.Client, kubeClient kubernetes.Interface, tracer trace.Tracer) *LogCollector {
	return &LogCollector{client: c, kubeClient: kubeClient, tracer: tracer}
}

// Collect fetches logs from VM pods and CNV operator pods based on the LogCollectionSpec.
func (lc *LogCollector) Collect(
	ctx context.Context,
	spec srev1alpha1.LogCollectionSpec,
	targetNamespaces []string,
) (*LogCollection, error) {
	ctx, span := lc.tracer.Start(ctx, "LogCollector.Collect")
	defer span.End()

	collection := &LogCollection{CollectedAt: time.Now().UTC()}

	if spec.CollectVMPodLogs {
		vmLogs, errs := lc.collectVMPodLogs(ctx, targetNamespaces, spec)
		collection.VMPodLogs = vmLogs
		collection.Errors = append(collection.Errors, errs...)
	}

	if spec.CollectOperatorLogs {
		opLogs, errs := lc.collectOperatorLogs(ctx, spec)
		collection.OperatorLogs = opLogs
		collection.Errors = append(collection.Errors, errs...)
	}

	span.SetAttributes(
		attribute.Int("vm_pod_logs.count", len(collection.VMPodLogs)),
		attribute.Int("operator_logs.count", len(collection.OperatorLogs)),
		attribute.Int("collection_errors.count", len(collection.Errors)),
	)
	return collection, nil
}

// collectVMPodLogs fetches logs from virt-launcher and virt-handler pods.
func (lc *LogCollector) collectVMPodLogs(
	ctx context.Context,
	namespaces []string,
	spec srev1alpha1.LogCollectionSpec,
) ([]PodLogEntry, []string) {
	logger := log.FromContext(ctx)
	var entries []PodLogEntry
	var errs []string

	// Pod label selectors for KubeVirt workload pods
	labelSelectors := []string{
		"kubevirt.io=virt-launcher",
		"kubevirt.io=virt-handler",
		"app=containerized-data-importer",
	}

	for _, ns := range namespaces {
		for _, selector := range labelSelectors {
			podList := &corev1.PodList{}
			listOpts := []client.ListOption{
				client.InNamespace(ns),
				client.MatchingLabels(parseSelector(selector)),
			}
			if err := lc.client.List(ctx, podList, listOpts...); err != nil {
				errs = append(errs, fmt.Sprintf("list pods (%s) in %s: %v", selector, ns, err))
				continue
			}

			for i := range podList.Items {
				pod := &podList.Items[i]
				podEntries, err := lc.collectPodLogs(ctx, pod, spec)
				if err != nil {
					logger.V(1).Info("Log collection failed for pod", "pod", pod.Name, "err", err)
					errs = append(errs, fmt.Sprintf("%s/%s: %v", pod.Namespace, pod.Name, err))
					continue
				}
				entries = append(entries, podEntries...)
			}
		}
	}

	return entries, errs
}

// collectOperatorLogs fetches logs from the openshift-cnv (HCO/KubeVirt operator) namespace.
func (lc *LogCollector) collectOperatorLogs(
	ctx context.Context,
	spec srev1alpha1.LogCollectionSpec,
) ([]PodLogEntry, []string) {
	logger := log.FromContext(ctx)
	var entries []PodLogEntry
	var errs []string

	// CNV operator namespaces and their pod selectors
	operatorSelectors := []struct {
		namespace string
		selector  string
	}{
		{"openshift-cnv", "app=hyperconverged-cluster-operator"},
		{"openshift-cnv", "app=virt-operator"},
		{"openshift-cnv", "name=cdi-operator"},
		{"openshift-cnv", "app=ssp-operator"},
		{"openshift-cnv", "app=node-maintenance-operator"},
		{"kubevirt", "kubevirt.io=virt-operator"},
	}

	for _, sel := range operatorSelectors {
		podList := &corev1.PodList{}
		listOpts := []client.ListOption{
			client.InNamespace(sel.namespace),
			client.MatchingLabels(parseSelector(sel.selector)),
		}
		if err := lc.client.List(ctx, podList, listOpts...); err != nil {
			// Namespace may not exist — silently skip
			continue
		}

		for i := range podList.Items {
			pod := &podList.Items[i]
			podEntries, err := lc.collectPodLogs(ctx, pod, spec)
			if err != nil {
				logger.V(1).Info("Operator log collection failed", "pod", pod.Name, "err", err)
				errs = append(errs, fmt.Sprintf("%s/%s: %v", pod.Namespace, pod.Name, err))
				continue
			}
			entries = append(entries, podEntries...)
		}
	}

	return entries, errs
}

// collectPodLogs fetches and filters logs for all containers in a pod.
func (lc *LogCollector) collectPodLogs(
	ctx context.Context,
	pod *corev1.Pod,
	spec srev1alpha1.LogCollectionSpec,
) ([]PodLogEntry, error) {
	var entries []PodLogEntry

	// Determine which containers to collect from
	containers := make([]string, 0)
	for _, c := range pod.Spec.Containers {
		containers = append(containers, c.Name)
	}
	// Also check init containers if collecting for failed pods
	if pod.Status.Phase == corev1.PodFailed {
		for _, c := range pod.Spec.InitContainers {
			containers = append(containers, c.Name)
		}
	}

	tailLines := int64(maxLogLines)
	if spec.TailLines > 0 {
		tailLines = int64(spec.TailLines)
	}

	sinceSeconds := int64(300) // default: last 5 minutes
	if spec.SinceSeconds > 0 {
		sinceSeconds = int64(spec.SinceSeconds)
	}

	for _, container := range containers {
		// Use an inline func so cancel() fires immediately after each
		// container's stream closes, not when collectPodLogs returns.
		// The original defer cancel() inside a loop would accumulate N
		// uncancelled contexts until the entire function returned.
		containerEntries := func(containerName string) []PodLogEntry {
			reqCtx, cancel := context.WithTimeout(ctx, logCollectTimeout)
			defer cancel()

			req := lc.kubeClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container:    containerName,
				TailLines:    &tailLines,
				SinceSeconds: &sinceSeconds,
				Timestamps:   true,
			})

			stream, err := req.Stream(reqCtx)
			if err != nil {
				// Container may not be running yet — non-fatal
				return nil
			}
			defer stream.Close()

			return filterLogLines(stream, pod.Namespace, pod.Name, containerName, spec)
		}(container)

		entries = append(entries, containerEntries...)
	}

	return entries, nil
}

// filterLogLines reads a log stream line by line, retaining only error-bearing lines.
func filterLogLines(
	r io.Reader,
	namespace, podName, containerName string,
	spec srev1alpha1.LogCollectionSpec,
) []PodLogEntry {
	var entries []PodLogEntry
	now := time.Now().UTC()

	scanner := bufio.NewScanner(r)
	// Expand scanner buffer for potentially long log lines
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 512*1024)

	// Compile custom patterns from spec
	customPatterns := make([]*regexp.Regexp, 0, len(spec.ErrorPatterns))
	for _, p := range spec.ErrorPatterns {
		if re, err := regexp.Compile(p); err == nil {
			customPatterns = append(customPatterns, re)
		}
	}

	allPatterns := append(errorPatterns, customPatterns...)

	count := 0
	for scanner.Scan() {
		if count >= maxErrLinesPerContainer {
			break
		}
		line := scanner.Text()
		if !matchesAnyPattern(line, allPatterns) {
			continue
		}
		// Strip the leading timestamp added by Kubernetes (RFC3339Nano)
		cleanedLine := stripKubeTimestamp(line)

		entries = append(entries, PodLogEntry{
			Namespace:     namespace,
			PodName:       podName,
			ContainerName: containerName,
			Line:          cleanedLine,
			CollectedAt:   now,
		})
		count++
	}

	return entries
}

// ToCheckResults converts a LogCollection's error lines into CheckResults
// so they surface in the SREPolicy status findings alongside other checks.
func (lc *LogCollection) ToCheckResults(checkName string) []srev1alpha1.CheckResult {
	var results []srev1alpha1.CheckResult
	now := metav1.Now()

	seen := make(map[string]bool)

	for _, entry := range append(lc.VMPodLogs, lc.OperatorLogs...) {
		ref := fmt.Sprintf("%s/%s[%s]", entry.Namespace, entry.PodName, entry.ContainerName)
		key := ref + ":" + truncate(entry.Line, 80)
		if seen[key] {
			continue // deduplicate near-identical lines from the same container
		}
		seen[key] = true

		results = append(results, srev1alpha1.CheckResult{
			CheckName:   checkName,
			Category:    srev1alpha1.CheckCategoryLogs,
			ResourceRef: ref,
			Status:      "Degraded",
			Severity:    classifyLogSeverity(entry.Line),
			Message:     fmt.Sprintf("Log error: %s", entry.Line),
			LastChecked: &now,
		})
	}

	for _, errStr := range lc.Errors {
		results = append(results, srev1alpha1.CheckResult{
			CheckName:   checkName,
			Category:    srev1alpha1.CheckCategoryLogs,
			ResourceRef: "log-collector/error",
			Status:      "Unknown",
			Severity:    srev1alpha1.SeverityWarning,
			Message:     fmt.Sprintf("Log collection error: %s", errStr),
			LastChecked: &now,
		})
	}

	return results
}

// ── helpers ──────────────────────────────────────────────────────────────────

func matchesAnyPattern(line string, patterns []*regexp.Regexp) bool {
	for _, re := range patterns {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

func classifyLogSeverity(line string) srev1alpha1.SeverityLevel {
	lower := strings.ToLower(line)
	if strings.Contains(lower, "panic") ||
		strings.Contains(lower, "fatal") ||
		strings.Contains(lower, "oom") ||
		strings.Contains(lower, "kill") {
		return srev1alpha1.SeverityCritical
	}
	return srev1alpha1.SeverityWarning
}

// stripKubeTimestamp removes the RFC3339Nano prefix that kubectl/API adds to log lines.
func stripKubeTimestamp(line string) string {
	// Format: "2024-01-15T10:30:00.123456789Z <actual log line>"
	if len(line) > 30 && line[10] == 'T' {
		if idx := strings.Index(line, " "); idx > 0 {
			return strings.TrimSpace(line[idx+1:])
		}
	}
	return line
}

// parseSelector converts "key=value" to map[string]string.
func parseSelector(selector string) map[string]string {
	m := make(map[string]string)
	for _, part := range strings.Split(selector, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			m[kv[0]] = kv[1]
		}
	}
	return m
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// FormatReport returns a compact text summary of the log collection for ConfigMap storage.
func (lc *LogCollection) FormatReport() string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Log Collection — %s\n", lc.CollectedAt.Format(time.RFC3339))
	fmt.Fprintf(&buf, "VM Pod Log Entries: %d\n", len(lc.VMPodLogs))
	fmt.Fprintf(&buf, "Operator Log Entries: %d\n", len(lc.OperatorLogs))
	if len(lc.Errors) > 0 {
		fmt.Fprintf(&buf, "Collection Errors: %d\n---\n", len(lc.Errors))
		for _, e := range lc.Errors {
			fmt.Fprintln(&buf, "  ERR:", e)
		}
	}
	if len(lc.VMPodLogs) > 0 {
		fmt.Fprintln(&buf, "--- VM Pod Errors (sample) ---")
		for i, e := range lc.VMPodLogs {
			if i >= 20 {
				fmt.Fprintf(&buf, "  ... (%d more)\n", len(lc.VMPodLogs)-20)
				break
			}
			fmt.Fprintf(&buf, "  [%s/%s] %s\n", e.PodName, e.ContainerName, e.Line)
		}
	}
	if len(lc.OperatorLogs) > 0 {
		fmt.Fprintln(&buf, "--- Operator Errors (sample) ---")
		for i, e := range lc.OperatorLogs {
			if i >= 20 {
				fmt.Fprintf(&buf, "  ... (%d more)\n", len(lc.OperatorLogs)-20)
				break
			}
			fmt.Fprintf(&buf, "  [%s/%s] %s\n", e.PodName, e.ContainerName, e.Line)
		}
	}
	return buf.String()
}
