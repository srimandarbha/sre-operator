# OpenShift Virtualization SRE Operator

The **OpenShift Virtualization SRE Operator** is a Kubernetes-native controller designed to monitor, diagnose, and remediate OpenShift Virtualization environments. It ensures high availability and smooth operation of virtual machines (VMs) and the underlying OpenShift cluster infrastructure.

This operator utilizes a declarative custom resource, `SREPolicy`, to define checks, alert triggers, log collection settings, and automated remediation actions.

---

## 🚀 Features

- **Automated Health Checks:** Define scheduled checks for VMs, Nodes, Storage, Network, and the OpenShift Cluster itself.
- **Alert-based Triggers:** Seamlessly integrate with Prometheus and Alertmanager to trigger automatic diagnoses or remediations when specific alerts fire.
- **Log Collection & Diagnostics:** Automatically collect and tail logs from target pods (e.g., `virt-launcher` pods) based on error patterns or alert triggers.
- **Automated Remediation:** Built-in actions to respond to failures:
  - `None` (Observability only)
  - `Diagnose` (Trigger log collection and diagnostic aggregation)
  - `Alert` (Forward events/alerts)
  - `Restart`, `Migrate`, `Drain`, `Evict` (Lifecycle operations on VMs/Nodes)
- **OpenTelemetry Tracing:** End-to-end distributed tracing for check executions and remediation actions.

---

## 🏗️ Architecture

The project is structured following standard Kubebuilder conventions:

- `api/v1alpha1`: Contains the `SREPolicy` Custom Resource Definition (CRD) and Go schema types.
- `internal/controller`: Contains the main reconciliation loop (`SREPolicyReconciler`) that manages the lifecycle of the policy.
- `internal/checks`: Pluggable health checkers for different categories (VMs, Nodes, Storage, etc.).
- `internal/remediation`: Evaluates findings and executes configured remediation actions.
- `internal/diagnostics`: Collects and aggregates logs and events.
- `internal/prometheus`: Connects to Prometheus to evaluate active metrics and receive alerts.
- `internal/telemetry`: OpenTelemetry integration for distributed tracing.

---

## 📝 Usage

### SREPolicy Custom Resource

The core of the operator is the `SREPolicy` Custom Resource. By creating an `SREPolicy` in your cluster, you instruct the operator on what to monitor and how to react.

**Example `SREPolicy`:**

```yaml
apiVersion: sre.kubevirt.io/v1alpha1
kind: SREPolicy
metadata:
  name: prod-vm-sre-policy
  namespace: openshift-cnv
spec:
  targetNamespaces:
    - default
    - production-vms
  
  # Configure OpenTelemetry Tracing
  openTelemetry:
    enabled: true
    endpoint: "otel-collector.observability.svc:4317"

  # Prometheus Alert Triggers
  prometheus:
    enabled: true
    prometheusUrl: "http://prometheus-operated.openshift-monitoring.svc:9090"
    alertTriggers:
      - name: "HighVMCpuUsage"
        alertName: "KubeVirtVMHighCPU"
        enabled: true
        remediation: "Diagnose"
        collectLogs: true

  # Log Collection Strategy
  logCollection:
    enabled: true
    collectVMPodLogs: true
    tailLines: 500
    onAlertOnly: true
    errorPatterns:
      - "OOMKilled"
      - "Connection reset by peer"

  # Active Health Checks
  checks:
    - name: "VMStatusCheck"
      category: "VM"
      enabled: true
      intervalSeconds: 60
      severity: "Warning"
      remediation: "None"
```

---

## 🛠️ Development Setup

To build and run this operator locally or deploy it to a cluster:

### Prerequisites
- Go 1.22+
- Access to an OpenShift cluster with OpenShift Virtualization installed.

### Build
```bash
# Tidy modules
go mod tidy

# Build the operator binary
go build -o bin/manager main.go
```

### Note on Binaries
Please be careful not to commit compiled binaries (like `manager.exe` or `kubebuilder.exe`) to the repository. It is highly recommended to add a `.gitignore` to prevent tracking large binary files.

---

## ⚠️ Open Issues & Production Readiness

The current state of the repository contains the structural foundation and core control loop. The following items must be implemented before the operator is considered fully production-ready:

- **Implement Concrete Checks**: Replace the dummy interfaces in `internal/checks/checks.go` with actual Kubernetes API queries (e.g., querying `VirtualMachineInstance` objects).
- **Implement Remediation Engine**: Implement the logic in `internal/remediation/remediation.go` to execute actions like Node Drains, VM Restarts, or Live Migrations.
- **Implement Log Collection**: Wire up the Kubernetes Pod log stream APIs in `internal/diagnostics/logcollector.go` to extract and parse actual pod logs.
- **RBAC Generation**: Add Kubebuilder RBAC markers (`//+kubebuilder:rbac:groups=...`) to generate `Role` and `ClusterRole` manifests.
- **Unit and E2E Tests**: Add `envtest` suites and E2E tests validating behavior against a live OpenShift Virtualization environment.
