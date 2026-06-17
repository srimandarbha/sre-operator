# SRE Operator — Production Readiness, Load Analysis & Alert Onboarding Guide

---

## 1. Production Readiness Audit

### Bugs found and fixed in this session

| # | Location | Bug | Severity | Fix applied |
|---|---|---|---|---|
| 1 | `internal/diagnostics/logcollector.go` | `defer cancel()` inside a `for` loop — cancels accumulated until function returns, not per iteration. Causes all but the last container stream to run without a functioning deadline. | **High** | Wrapped each iteration in an inline closure with its own `defer cancel()` |
| 2 | `controllers/srepolicy_controller.go` | `prometheus.NewClient()` called every reconcile cycle, discarding the HTTP connection pool each time. At 30s intervals this creates ~2 TCP connections/min to Thanos and AlertManager with no reuse. | **High** | `promClient` moved to reconciler struct, rebuilt only when spec changes |
| 3 | `controllers/srepolicy_controller.go` | `telemetry.NewProvider()` (which dials a gRPC connection) called every reconcile cycle. Old provider never `Shutdown()`'d → goroutine + connection leak per cycle. | **High** | Re-init gated on `spec.openTelemetry.endpoint` change |
| 4 | `controllers/srepolicy_controller.go` | `status.Findings` written with the full unbounded slice every cycle. With 10 namespaces × 4 checkers × many VMs this can exceed etcd's 1.5MB object limit and cause update failures. | **Medium** | Capped at 500 findings in status (sorted by severity). Full set always in `sre-diagnostics` ConfigMap. |
| 5 | `internal/prometheus/client.go` | `http.Transport` had no connection pool settings, using Go defaults (unlimited idle conns, no timeout). | **Low** | Set `MaxIdleConnsPerHost: 2`, `IdleConnTimeout: 90s` |

### What is production-ready as-is

- **controller-runtime client** for all Kubernetes LIST/GET calls goes through the shared informer cache. Zero direct API server calls per reconcile for pods, nodes, PVCs.
- **Unstructured client** for CO, CV, MCP — no openshift/api vendor dep, works across OCP versions.
- **OTel tracing** wraps every check and remediation with spans. TraceID injected into every finding and Kubernetes Event.
- **Finalizer** prevents the CR from being deleted while remediations are in progress.
- **Leader election** — only one operator replica acts at a time.
- **Cool-down guard** on alert-triggered remediations prevents thundering-herd on flapping alerts.
- **Max attempt guard** stops infinite remediation loops.
- **Non-fatal degradation** — every check failure is logged and skipped, never blocks the cycle.

### What needs work before a first production release

| Area | Gap | Recommendation |
|---|---|---|
| **Tests** | Zero unit or integration tests written | Add table-driven tests for each checker using `fake.NewClientBuilder()`. Add envtest integration test for the reconcile loop. |
| **DeepCopyObject** | (FIXED) DeepCopy methods implemented | Manually implemented DeepCopy methods in types files to bypass dependencies. |
| **CRD validation** | `CheckCategory` enum in kubebuilder marker doesn't include `Prometheus`, `OCPCluster`, `Logs` (added as consts, not in the enum tag) | Update the `+kubebuilder:validation:Enum` marker and regenerate CRD |
| **Secret mounting** | `BearerTokenSecretRef` for Prometheus described in spec but no VolumeMount added to `manager.yaml` | Add projected volume mount in manager Deployment |
| **RBAC for Prometheus** | No RBAC for accessing the `openshift-monitoring` namespace Thanos/AlertManager routes | Add `Role` in `openshift-monitoring` for the SA, or use a route with the SA token |
| **Status conflict** | `r.Status().Update()` can conflict under high churn — no retry on conflict | Wrap in `retry.RetryOnConflict` |
| **Findings deduplication** | Same finding (same resourceRef + message) can appear multiple times across namespaces | Add a dedup pass before writing status |
| **Log collection RBAC** | `pods/log` GET is cluster-scoped in ClusterRole but log collection targets specific namespaces | Scope to target namespaces with a Role, not ClusterRole |

---

## 2. Load on Prometheus and AlertManager

### Calls per reconcile cycle (default: every 60 seconds)

| Call | Target | Count | Notes |
|---|---|---|---|
| `GET /api/v2/alerts` | AlertManager | **1** | One call fetches all alerts; filtered locally in-process |
| `GET /api/v1/query` | Thanos querier | **N** | One call per `metricQueries[*]` entry |

With the default production sample (4 metric queries): **5 HTTP calls per 60s cycle** to Prometheus/AlertManager.

AlertManager `/api/v2/alerts` is an in-memory read with no TSDB involvement. Cost is negligible.

Each PromQL instant query hits Thanos querier, which may fan out to store-gateways if the query touches long-range data. **Use `rate(metric[5m])` not `rate(metric[1h])` to keep queries cheap.**

### Calls that do NOT hit Prometheus

All Kubernetes object reads (pods, nodes, PVCs, ClusterOperators, etc.) go through the **controller-runtime informer cache** — a local in-memory copy kept current by a Watch. Zero API server calls per reconcile for reads.

### Recommended PromQL hygiene

```yaml
# BAD — scans full TSDB history, expensive on Thanos
expr: 'increase(kubevirt_vmi_phase_transition_seconds_count[24h])'

# GOOD — only reads the last 5 minutes of data
expr: 'rate(kubevirt_vmi_phase_transition_seconds_count[5m])'

# GOOD — add namespace selector to reduce cardinality
expr: 'kubevirt_vmi_memory_available_bytes{namespace=~"vm-workloads|vm-databases"} < 1e9'
```

---

## 3. Load on the API Server

### Reads — zero direct calls

Every `client.List()` and `client.Get()` in the checkers uses the **controller-runtime delegating client**, which reads from the shared informer cache populated by Watches. The cache is established at startup, not rebuilt each cycle.

```
Reconcile() → checker.Run() → client.List(pods) 
                                     ↓
                          informer cache (in-memory)
                                     ↓ (no API server call)
```

### Writes — bounded per cycle

| Write | Trigger | Rate |
|---|---|---|
| `status.Update` on SREPolicy | Every cycle | 1 per cycle per policy |
| `configmap` patch (`sre-diagnostics`) | Every cycle | 1 per cycle |
| `configmap` patch (`sre-log-diagnostics`) | Only when alerts fire | 0–1 per cycle |
| `pod delete` (Restart) | Only when CrashLoop detected + remediation=Restart | Very rare |
| `node patch` (cordon) | Only Drain remediation | Very rare |
| VMIM create | Only Migrate remediation | Very rare |
| Eviction create | Only Drain/Evict | Very rare |
| Kubernetes Event create | Per non-healthy finding | Bounded by `maxStatusFindings` |

**The status update is the highest-frequency write.** With the findings cap at 500, the SREPolicy object stays under ~200KB, well within etcd limits.

### Watches established at startup

controller-runtime establishes one Watch per resource type the manager is configured for. These are shared across all reconciles — not per-cycle:

- `srepolicies` (the CRD itself)

All other types (pods, nodes, PVCs) are lazily watched by the informer cache when first listed.

---

## 4. How to Add a New Alert with Full Diagnostics and Remediation

This is a **YAML-only operation** for most cases. No Go code needed unless you need a custom remediation action or a new check type.

### Step 1 — Identify your alert

Find the `alertname` label in AlertManager:
```bash
# List all currently firing alerts
curl -sk -H "Authorization: Bearer $(oc sa get-token sre-operator -n openshift-cnv)" \
  https://alertmanager-main.openshift-monitoring.svc.cluster.local:9094/api/v2/alerts \
  | jq '.[].labels.alertname' | sort -u

# Or from the OCP console: Observe → Alerting → Alerts
```

### Step 2 — Choose your remediation action

| Action | What it does | When to use |
|---|---|---|
| `Diagnose` | Collects OCP cluster state + logs, no destructive action | **Always start here** for a new alert |
| `Alert` | Emits a Kubernetes Event only | When you want visibility but no automation |
| `Restart` | Deletes the virt-launcher pod, KubeVirt recreates it | VM-level crash or stuck boot |
| `Migrate` | Creates a VirtualMachineInstanceMigration CR | Node resource pressure, pre-maintenance |
| `Drain` | Cordons the node + evicts all virt-launcher pods | Node hardware issue, confirmed degraded |
| `Evict` | Evicts a specific pod | Targeted, less disruptive than Drain |

### Step 3 — Add the trigger to your SREPolicy

```yaml
spec:
  prometheus:
    enabled: true
    alertTriggers:

      # --- YOUR NEW ALERT ---
      - name: my-vmi-timeout          # unique name, used in findings + logs
        alertName: KubeVirtVMIPhaseTransitionTimeout   # exact Prometheus alertname

        # Optional: only match alerts with these additional labels
        labelSelectors:
          namespace: vm-workloads     # only for VMs in this namespace
          severity: critical          # only critical-severity instances

        enabled: true

        # Start with Diagnose — promotes to destructive only after you're
        # confident the alert is actionable and the target derivation is correct
        remediation: Diagnose         # change to Restart/Migrate/etc later

        # Safety: don't act until the alert has been firing for 5 minutes
        # Prevents action on transient spikes
        maxFiringDurationMinutes: 5

        # Diagnostics collected automatically when this alert fires:
        collectLogs: true             # virt-launcher + operator logs
        collectOCPClusterState: true  # ClusterOperator / MCP snapshot

        runbookURL: "https://runbooks.example.com/vmi-phase-timeout"
```

### Step 4 — Enable log collection and OCP state capture

These fire automatically when `collectLogs: true` and `collectOCPClusterState: true` are set on the trigger. Make sure these top-level specs are also present:

```yaml
spec:
  ocpCluster:
    enabled: true
    checkClusterOperators: true
    checkClusterVersion: true
    checkMachineConfigPools: true

  logCollection:
    enabled: true
    collectVMPodLogs: true
    collectOperatorLogs: true
    tailLines: 200
    sinceSeconds: 300
    onAlertOnly: true               # only collect when an alert trigger fires
    persistToConfigMap: true
    errorPatterns:
      - "(?i)phase transition timeout"   # add patterns specific to your alert
```

### Step 5 — Apply and observe

```bash
kubectl apply -f config/samples/srepolicy_production.yaml

# Watch for the alert to fire
kubectl get srep production-virt-sre -n openshift-cnv -w

# When it fires, check:
kubectl get srep production-virt-sre -n openshift-cnv \
  -o jsonpath='{.status.firingAlertNames}'

# See the finding with TraceID
kubectl get srep production-virt-sre -n openshift-cnv \
  -o jsonpath='{.status.findings}' | jq '.[] | select(.category=="Prometheus")'

# Read the log diagnostic summary
kubectl get cm sre-log-diagnostics -n openshift-cnv \
  -o jsonpath='{.data.log-summary\.txt}'

# Read the full diagnostic report (includes OCP cluster state snapshot)
kubectl get cm sre-diagnostics -n openshift-cnv \
  -o jsonpath='{.data.latest-report\.json}' | jq '{
    reportId,
    otelTraceId,
    "firing": .criticalFindings | map(select(.category=="Prometheus")),
    "ocpIssues": .criticalFindings | map(select(.category=="OCPCluster")),
    "logErrors": (.findings | map(select(.category=="Logs")) | length)
  }'
```

### Step 6 — Graduate from Diagnose to a real remediation

After you've seen several cycles of the alert and confirmed:
1. The `resourceRef` in the finding correctly identifies the VM or node
2. The `DiagnosticHints` (alert annotations) contain enough context
3. The OCP cluster state and logs collected are actionable

Change `remediation: Diagnose` → `remediation: Restart` (or whichever action fits) and re-apply. The `maxFiringDurationMinutes` cool-down guard remains active.

```bash
# Verify the remediation was applied
kubectl get srep production-virt-sre -n openshift-cnv \
  -o jsonpath='{.status.findings}' \
  | jq '.[] | select(.remediationApplied != "") | {resourceRef, remediationApplied, remediationAttempts}'

# Kubernetes Event with TraceID for correlation
kubectl get events -n openshift-cnv \
  --field-selector reason=SREPrometheusailed \
  --sort-by='.lastTimestamp'
```

### Adding a new PromQL metric threshold check

No alert in AlertManager for your condition? Define a raw PromQL check instead:

```yaml
spec:
  prometheus:
    metricQueries:
      - name: vm-network-drop-rate
        expr: |
          rate(kubevirt_vmi_network_receive_packets_dropped_total[5m]) > 0
        description: "VM is dropping inbound packets — possible bridge or OVN issue"
        operator: gt
        warningThreshold: 10      # >10 drops/s = Warning
        criticalThreshold: 100    # >100 drops/s = Critical
        enabled: true
        remediation: Alert        # start with Alert, escalate after investigation
        runbookURL: "https://runbooks.example.com/vm-network-drops"
```

This runs one `GET /api/v1/query` per cycle to Thanos and produces a `CheckResult` with `category: Prometheus` that flows through the same findings pipeline as built-in checks.

---

## 5. API Server and Prometheus Call Budget (example config)

With the production sample as configured:

```
Every 60 seconds:
  API server writes:    2  (status update + ConfigMap patch)
  API server reads:     0  (all from informer cache)
  AlertManager calls:   1  (GET /api/v2/alerts — fetches all, filtered locally)
  Thanos/Prom calls:    4  (one per metricQuery entry)
  Pod log streams:      0  (only when alert fires + OnAlertOnly=true)

When an alert fires (additional):
  Pod log streams:      ~6  (virt-launcher × 2 namespaces + 4 operator pods)
  API server writes:    1  (log-diagnostics ConfigMap)
```

**Total steady-state: ~7 outbound calls per 60-second cycle.** This is negligible load on any production cluster.
