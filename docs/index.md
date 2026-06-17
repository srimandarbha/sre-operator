# SRE Operator Documentation

Welcome to the documentation for the **SRE Operator**.

The SRE Operator acts as an automated Site Reliability Engineer within your OpenShift/Kubernetes cluster. It constantly monitors your infrastructure (Virtual Machines, Storage, Network, and Nodes), evaluates Prometheus alerts, and can automatically execute complex runbooks to mitigate and resolve issues without human intervention.

## Core Concepts

The operator is entirely driven by a Custom Resource Definition (CRD) called `SREPolicy`. This policy dictates:
1. What resources and namespaces to monitor.
2. Which built-in checks and Prometheus alerts to evaluate.
3. What multi-step remediations (Directed Acyclic Graphs or DAGs) to execute when failures occur.

## Getting Started

Check out the following guides to learn how to configure and use the SRE Operator:

* [SREPolicy CRD Reference](srepolicy-crd.md) - Learn how to write your configuration policies.
* [Workflows & Reusable Actions](workflows-and-actions.md) - Learn how to build complex, multi-step remediation runbooks.
