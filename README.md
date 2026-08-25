# Sealos State Metrics

[![Go Report Card](https://goreportcard.com/badge/github.com/labring/sealos-state-metrics)](https://goreportcard.com/report/github.com/labring/sealos-state-metrics)
[![License](https://img.shields.io/github/license/labring/sealos-state-metrics)](https://github.com/labring/sealos-state-metrics/blob/main/LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/labring/sealos-state-metrics?filename=go.mod)](https://github.com/labring/sealos-state-metrics/blob/main/go.mod)
[![Build Status](https://img.shields.io/github/actions/workflow/status/labring/sealos-state-metrics/release.yml?branch=main)](https://github.com/labring/sealos-state-metrics/actions)

A highly extensible Kubernetes metrics collector that provides Prometheus-compatible monitoring for cluster resources, infrastructure components, and external systems. Built with a modular plugin architecture, hot-reload capability, and dynamic CRD monitoring support.

## Overview

Sealos State Metrics is designed as a modern alternative to kube-state-metrics with significant enhancements:

- **Modular Plugin Architecture**: Enable only the collectors you need, add new collectors without modifying core code
- **Hot Configuration Reload**: Update configuration and TLS certificates without pod restarts
- **Dynamic CRD Monitoring**: Monitor any Custom Resource Definition through runtime configuration, no code changes required
- **Unified Management**: Replace multiple scattered exporters with a single, consistent monitoring solution
- **Flexible Deployment**: Support both DaemonSet (node-level) and Deployment (cluster-level) modes
- **Leader Election Support**: Fine-grained control per collector to avoid duplicate metrics
- **External System Monitoring**: Built-in support for databases, domains, cloud accounts, and more

## Key Features

### 🔌 Extensible Collector System

Built on a factory-based registration pattern inspired by Prometheus node_exporter:

- **8+ Built-in Collectors**: Domain health, node conditions, database connectivity, LVM storage, zombie processes, image pull tracking, cloud balances, and more
- **Easy to Extend**: Add custom collectors by implementing a simple interface
- **Lazy Initialization**: Collectors are only instantiated when enabled
- **Lifecycle Management**: Unified start/stop/health check interfaces

### 🔥 Hot Configuration Reload

Zero-downtime configuration updates:

- **File-based Configuration**: YAML configuration with automatic reload on changes
- **Kubernetes ConfigMap Support**: Detects ConfigMap updates via symlink changes
- **TLS Certificate Reload**: Automatically picks up cert-manager certificate rotations
- **Debouncing**: Intelligent 3-second delay to handle Kubernetes atomic updates
- **Partial Reload**: Some settings (logging, debug server, pprof) reload without stopping collectors

### 🎯 Dynamic CRD Monitoring

Monitor any Custom Resource Definition without code changes:

```yaml
collectors:
  crds:
    crds:
      - name: "my-application"
        gvr:
          group: "apps.example.com"
          version: "v1"
          resource: "applications"
        metrics:
          - type: "gauge"
            name: "app_replicas"
            path: "spec.replicas"
          - type: "conditions"
            name: "app_condition"
            path: "status.conditions"
```

**Supported Metric Types:**
- `info`: Metadata labels (value always 1)
- `gauge`: Numeric values from resource fields
- `count`: Aggregate counts by field value
- `string_state`: Current state as a label
- `map_state`: State metrics for map entries
- `map_gauge`: Numeric values from maps
- `conditions`: Kubernetes-style condition arrays

### 🎛️ Flexible Configuration

Three-layer configuration priority system:

1. **Defaults**: Hard-coded in each collector
2. **YAML File**: Loaded at startup and on reload
3. **Environment Variables**: Highest priority, perfect for containerized environments

```yaml
# config.yaml
server:
  address: ":9090"

logging:
  level: "info"
  format: "text"

leaderElection:
  enabled: true
  leaseDuration: "15s"

collectors:
  domain:
    domains:
      - endpoint: example.com
        skipTLSVerify: false
    checkInterval: "1m"
    checkTimeout: "30s"
    dialRetries: 3
    includeIPv4: true
    includeIPv6: true
```

### 🏗️ Production-Ready Architecture

**Deployment Modes:**

**DaemonSet Mode** (recommended for node-level monitoring):
```yaml
# Each node runs collectors independently
# Non-leader collectors: lvm, zombie (node-specific)
# Leader collectors: domain, database, node (cluster-wide)
```

**Deployment Mode** (multiple replicas with leader election):
```yaml
# All replicas run the same collectors
# Leader election ensures only one instance collects metrics
# Better for pure cluster-level monitoring
```

**Leader Election:**
- Granular control: each collector declares if it needs leader election
- Prevents duplicate metrics for cluster-wide resources
- Automatic failover when leader pod dies
- Configurable lease duration and renewal

## Available Collectors

| Collector | Type | Leader Election | Description |
|-----------|------|-----------------|-------------|
| **[domain](pkg/collector/domain/)** | Polling | Yes | Domain health checks, TLS certificate expiry, HTTP connectivity, DNS resolution |
| **[node](pkg/collector/node/)** | Informer | Yes | Kubernetes node conditions (Ready, MemoryPressure, DiskPressure, etc.) |
| **[database](pkg/collector/database/)** | Polling | Yes | Database connectivity monitoring (MySQL, PostgreSQL, MongoDB, Redis) via KubeBlocks |
| **[cockroachlicense](pkg/collector/cockroachlicense/)** | Polling | Yes | CockroachDB license metadata and expiry monitoring |
| **[imagepull](pkg/collector/imagepull/)** | Informer | Yes | Container image pull performance, slow pull detection, pull failure tracking |
| **[zombie](pkg/collector/zombie/)** | Polling | Yes | Zombie (defunct) process detection in containers |
| **[lvm](pkg/collector/lvm/)** | Polling | No | LVM storage metrics, volume group capacity and usage (node-level) |
| **[cloudbalance](pkg/collector/cloudbalance/)** | Polling | Yes | Cloud provider account balance (Alibaba Cloud, Tencent Cloud, VolcEngine) |
| **[userbalance](pkg/collector/userbalance/)** | Polling | Yes | Sealos user account balance from PostgreSQL database |
| **[crds](pkg/collector/crds/)** | Informer | Yes | Dynamic monitoring of any Custom Resource Definition |

> 💡 **New collectors are easy to add!** See [Creating Custom Collectors](#creating-custom-collectors) below.

## Quick Start

### Installation via Helm

```bash
# Add Helm repository (if available)
helm repo add sealos https://charts.sealos.io
helm repo update

# Install with default configuration
helm install sealos-state-metrics sealos/sealos-state-metrics \
  --namespace monitoring \
  --create-namespace

# Install with custom collectors
helm install sealos-state-metrics sealos/sealos-state-metrics \
  --namespace monitoring \
  --create-namespace \
  --set enabledCollectors="{domain,node,database,lvm}"
```

### Installation from Source

```bash
# Clone the repository
git clone https://github.com/labring/sealos-state-metrics.git
cd sealos-state-metrics

# Install using local Helm chart
helm install sealos-state-metrics ./deploy/charts/sealos-state-metrics \
  --namespace monitoring \
  --create-namespace \
  --values values-custom.yaml
```

### Docker Deployment

```bash
# Run locally (requires kubeconfig)
docker run -d \
  --name sealos-state-metrics \
  -p 9090:9090 \
  -v ~/.kube/config:/root/.kube/config:ro \
  -v $(pwd)/config.yaml:/etc/sealos-state-metrics/config.yaml:ro \
  ghcr.io/labring/sealos-state-metrics:latest \
  --config=/etc/sealos-state-metrics/config.yaml
```

## Configuration

### Basic Configuration

Create a `config.yaml` file:

```yaml
# Server configuration
server:
  address: ":9090"
  metricsPath: "/metrics"
  healthPath: "/health"

# Logging
logging:
  level: "info"  # debug, info, warn, error
  format: "text" # json, text

# Leader election (required for cluster-level collectors)
leaderElection:
  enabled: true
  leaseName: "sealos-state-metrics"
  leaseDuration: "15s"
  renewDeadline: "10s"
  retryPeriod: "2s"

# Metrics namespace (prefix for all metrics)
metrics:
  namespace: "sealos"

# Enable collectors
enabledCollectors:
  - domain
  - node
  - database
  - lvm

# Collector-specific configuration
collectors:
  domain:
    domains:
      - example.com
      - endpoint: internal.example.local:8443
        skipTLSVerify: true
      - api.example.com
    checkInterval: "1m"
    checkTimeout: "30s"
    dialRetries: 3
    includeIPv4: true
    includeIPv6: true
    includeCertCheck: true
    includeHTTPCheck: true

  database:
    checkInterval: "5m"
    checkTimeout: "10s"
    namespaces: []  # Empty = all namespaces

  lvm:
    updateInterval: "10s"
```

### Environment Variable Overrides

All configuration can be overridden using environment variables:

```bash
# Global settings
export SERVER_ADDRESS=":8080"
export LOGGING_LEVEL="debug"
export LEADER_ELECTION_ENABLED="false"

# Collector settings
export COLLECTORS_DOMAIN_CHECK_INTERVAL="1m"
export COLLECTORS_DOMAIN_DIAL_RETRIES="3"
export COLLECTORS_DOMAIN_DOMAINS="example.com,test.com"
export COLLECTORS_DATABASE_NAMESPACES="default,production"

# Arrays use comma-separated values
export ENABLED_COLLECTORS="domain,node,lvm"
```

### Configuration Hot Reload

**What can be reloaded:**
- Logging configuration (level, format)
- Debug server (enable/disable, port)
- Pprof server (enable/disable, port)
- All collector configurations
- Enabled collectors list

**What requires restart:**
- Main server address and port
- TLS configuration
- Authentication settings

**Trigger reload:**
```bash
# Update ConfigMap (Kubernetes will trigger reload automatically)
kubectl edit configmap sealos-state-metrics-config

# Or replace the config file and wait 3 seconds
kubectl create configmap sealos-state-metrics-config \
  --from-file=config.yaml \
  --dry-run=client -o yaml | kubectl apply -f -
```

## Monitoring Integration

### Prometheus Operator

Enable ServiceMonitor in Helm values:

```yaml
serviceMonitor:
  enabled: true
  namespace: monitoring
  interval: 30s
  scrapeTimeout: 10s
  labels:
    prometheus: kube-prometheus
```

### VictoriaMetrics Operator

Enable VMServiceScrape in Helm values:

```yaml
vmServiceScrape:
  enabled: true
  namespace: monitoring
  interval: 30s
  scrapeTimeout: 10s
```

### Manual Prometheus Configuration

```yaml
scrape_configs:
  - job_name: 'sealos-state-metrics'
    static_configs:
      - targets: ['sealos-state-metrics.monitoring.svc:9090']
    scrape_interval: 30s
    scrape_timeout: 10s
```

## Metrics Examples

### Framework Metrics

All collectors expose self-monitoring metrics:

```promql
# Collector execution duration
state_metric_collector_duration_seconds{collector="domain"} 0.152

# Collector success status (1=success, 0=failure)
state_metric_collector_success{collector="database"} 1

# Last collection timestamp
state_metric_collector_last_collection_timestamp{collector="node"} 1699000000
```

### Domain Collector Metrics

```promql
# Domain health status
sealos_domain_health{domain="example.com",type="resolve"} 1
sealos_domain_health{domain="example.com",type="healthy_ips"} 2

# Certificate expiry (seconds until expiration)
sealos_domain_cert_expiry_seconds{domain="example.com",ip="1.2.3.4"} 2592000

# Response time
sealos_domain_response_time_seconds{domain="example.com",ip="1.2.3.4"} 0.125
```

### Database Collector Metrics

```promql
# Database connectivity (1=connected, 0=disconnected)
sealos_database_connectivity{namespace="default",database="mysql1",type="mysql"} 1

# Connection response time
sealos_database_response_time_seconds{namespace="default",database="mysql1",type="mysql"} 0.089
```

### LVM Collector Metrics

```promql
# Total LVM capacity per node
sealos_lvm_vgs_total_capacity{node="worker-1"} 1099511627776

# Total free space per node
sealos_lvm_vgs_total_free{node="worker-1"} 549755813888

# Storage utilization
(sealos_lvm_vgs_total_capacity - sealos_lvm_vgs_total_free) / sealos_lvm_vgs_total_capacity * 100
```

## Creating Custom Collectors

Adding a new collector is straightforward. Here's a minimal example:

### 1. Create Directory Structure

```
pkg/collector/mycollector/
├── config.go
├── factory.go
└── mycollector.go
```

### 2. Define Configuration

```go
// config.go
package mycollector

import "time"

type Config struct {
    CheckInterval time.Duration `yaml:"checkInterval" env:"CHECK_INTERVAL"`
    Enabled       bool          `yaml:"enabled" env:"ENABLED"`
}

func NewDefaultConfig() *Config {
    return &Config{
        CheckInterval: 30 * time.Second,
        Enabled:       true,
    }
}
```

### 3. Implement Collector

```go
// mycollector.go
package mycollector

import (
    "context"
    "github.com/labring/sealos-state-metrics/pkg/collector/base"
    "github.com/prometheus/client_golang/prometheus"
)

type Collector struct {
    *base.BaseCollector
    config *Config
    myMetric *prometheus.Desc
}

func (c *Collector) Poll(ctx context.Context) error {
    // Fetch data and update metrics
    return nil
}
```

### 4. Create Factory

```go
// factory.go
package mycollector

import (
    "github.com/labring/sealos-state-metrics/pkg/collector"
    "github.com/labring/sealos-state-metrics/pkg/registry"
)

func init() {
    registry.MustRegister("mycollector", NewCollector)
}

func NewCollector(ctx *collector.FactoryContext) (collector.Collector, error) {
    cfg := NewDefaultConfig()
    ctx.ConfigLoader.LoadModuleConfig("collectors.mycollector", cfg)

    // Create and configure collector
    c := &Collector{
        BaseCollector: base.NewBaseCollector("mycollector", ctx.Logger),
        config: cfg,
    }

    return c, nil
}
```

### 5. Register in all.go

```go
// pkg/collector/all/all.go
import (
    _ "github.com/labring/sealos-state-metrics/pkg/collector/mycollector"
)
```

That's it! Your collector is now available. See existing collectors for more examples:
- **Simple polling**: [LVM Collector](pkg/collector/lvm/)
- **Informer-based**: [Node Collector](pkg/collector/node/)
- **Complex polling**: [Database Collector](pkg/collector/database/)

## Development

### Prerequisites

- Go 1.23+
- Docker (for building images)
- Kubernetes cluster (for testing)
- Helm 3+ (for chart installation)

### Building

```bash
# Build binary
make build

# Build Docker image
make docker-build

# Build and push Docker image
make docker-push REGISTRY=ghcr.io/yourusername

# Run tests
make test

# Run linters
make lint
```

### Local Development

```bash
# Run locally with kubeconfig
go run main.go \
  --config=config.example.yaml \
  --log-level=debug

# Build and run
make build
./bin/sealos-state-metrics --config=config.yaml
```

### Testing in Kubernetes

```bash
# Install from local chart
helm install sealos-state-metrics ./deploy/charts/sealos-state-metrics \
  --set image.repository=localhost:5000/sealos-state-metrics \
  --set image.tag=dev \
  --set image.pullPolicy=Always

# Watch logs
kubectl logs -f -l app.kubernetes.io/name=sealos-state-metrics

# Port forward for local testing
kubectl port-forward svc/sealos-state-metrics 9090:9090

# Test metrics endpoint
curl http://localhost:9090/metrics
```

## Architecture

### Component Overview

```
┌─────────────────────────────────────────────────────────────┐
│                     Main Server                             │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │ HTTP Server  │  │ TLS Handler  │  │ Auth Handler │      │
│  │ :9090        │  │ (cert-mgr)   │  │ (bearer)     │      │
│  └──────────────┘  └──────────────┘  └──────────────┘      │
└─────────────────────────────────────────────────────────────┘
                           │
                ┌──────────┴──────────┐
                │                     │
         ┌──────▼──────┐      ┌──────▼──────┐
         │ Debug Server│      │ Pprof Server│
         │ (localhost) │      │ (localhost) │
         └─────────────┘      └─────────────┘
                           │
         ┌─────────────────┴─────────────────┐
         │     Collector Registry            │
         │  (Manages all collectors)         │
         └───────────────┬───────────────────┘
                         │
         ┌───────────────┼───────────────┐
         │               │               │
    ┌────▼────┐    ┌────▼────┐    ┌────▼────┐
    │ Leader  │    │Non-Leader│   │ Informer│
    │Collectors│    │Collectors│   │  Cache  │
    │(domain, │    │  (lvm,   │   │         │
    │database)│    │ zombie)  │   │         │
    └─────────┘    └─────────┘    └─────────┘
```

### Configuration Flow

```
Priority: Defaults → YAML File → Environment Variables

┌──────────────┐
│ Hard-coded   │
│  Defaults    │──┐
└──────────────┘  │
                  ▼
┌──────────────┐  ┌──────────────┐
│ YAML Config  │─→│ ConfigLoader │
│   File       │  │ (composite)  │
└──────────────┘  └──────┬───────┘
                         │
┌──────────────┐         │
│ Environment  │─────────┤
│  Variables   │         │
└──────────────┘         ▼
                  ┌──────────────┐
                  │ Final Config │
                  └──────────────┘
```

### Hot Reload Flow

```
ConfigMap Update
      │
      ▼
┌──────────────┐    ┌──────────────┐
│  fsnotify    │───→│  Debouncer   │
│  Watcher     │    │  (3 seconds) │
└──────────────┘    └──────┬───────┘
                           │
                           ▼
                    ┌──────────────┐
                    │ Validate New │
                    │   Config     │
                    └──────┬───────┘
                           │
                    ┌──────▼───────┐
                    │ Stop All     │
                    │ Collectors   │
                    └──────┬───────┘
                           │
                    ┌──────▼───────┐
                    │ Reinitialize │
                    │ Collectors   │
                    └──────┬───────┘
                           │
                    ┌──────▼───────┐
                    │ Start        │
                    │ Collectors   │
                    └──────────────┘
```

## Comparison with kube-state-metrics

| Feature | kube-state-metrics | Sealos State Metrics |
|---------|-------------------|---------------------|
| **Kubernetes Resources** | ✅ All built-in resources | ✅ Informer-based collectors |
| **Custom Resources (CRD)** | ⚠️ Static (compile-time) | ✅ Dynamic (runtime config) |
| **Configuration** | ⚠️ CLI flags only | ✅ YAML + Env vars + Hot reload |
| **Hot Reload** | ❌ Not supported | ✅ Full support |
| **External Systems** | ❌ Kubernetes only | ✅ Databases, Domains, Cloud |
| **Plugin Architecture** | ❌ Monolithic | ✅ Modular collectors |
| **Deployment Modes** | ⚠️ Single mode | ✅ DaemonSet + Deployment |
| **Leader Election** | ⚠️ All-or-nothing | ✅ Per-collector control |
| **Memory Optimization** | ✅ Built-in | ✅ Transform support |

## Troubleshooting

### Check Pod Status

```bash
kubectl get pods -n monitoring -l app.kubernetes.io/name=sealos-state-metrics
kubectl describe pod -n monitoring -l app.kubernetes.io/name=sealos-state-metrics
```

### View Logs

```bash
# Follow logs
kubectl logs -n monitoring -l app.kubernetes.io/name=sealos-state-metrics -f

# Search for errors
kubectl logs -n monitoring -l app.kubernetes.io/name=sealos-state-metrics | grep -i error

# View specific collector logs
kubectl logs -n monitoring -l app.kubernetes.io/name=sealos-state-metrics | grep "collector=domain"
```

### Access Metrics Endpoint

```bash
# Port forward
kubectl port-forward -n monitoring svc/sealos-state-metrics 9090:9090

# Fetch metrics
curl http://localhost:9090/metrics

# Check specific collector metrics
curl http://localhost:9090/metrics | grep sealos_domain

# Check framework metrics
curl http://localhost:9090/metrics | grep state_metric_collector
```

### Health Check

```bash
# Check health endpoint
curl http://localhost:9090/health

# Expected response:
# {"status":"ok","collectors":{"domain":"healthy","node":"healthy"}}
```

### Leader Election Status

```bash
# Check lease
kubectl get lease -n monitoring sealos-state-metrics -o yaml

# Check holder identity
kubectl get lease -n monitoring sealos-state-metrics \
  -o jsonpath='{.spec.holderIdentity}'
```

### Common Issues

**Issue: Collector not starting**
```bash
# Check RBAC permissions
kubectl auth can-i get nodes --as=system:serviceaccount:monitoring:sealos-state-metrics

# View collector-specific logs
kubectl logs -n monitoring -l app.kubernetes.io/name=sealos-state-metrics | grep "collector=yourname"
```

**Issue: No metrics for a collector**
```bash
# Verify collector is enabled
kubectl get configmap -n monitoring sealos-state-metrics-config -o yaml

# Check collector health
curl http://localhost:9090/health
```

**Issue: Configuration not reloading**
```bash
# Check file watcher logs
kubectl logs -n monitoring -l app.kubernetes.io/name=sealos-state-metrics | grep "reload"

# Trigger manual restart (if needed)
kubectl rollout restart deployment/sealos-state-metrics -n monitoring
```

## Security Considerations

### RBAC Permissions

The application requires the following Kubernetes permissions:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: sealos-state-metrics
rules:
  # Core resources
  - apiGroups: [""]
    resources: ["nodes", "pods", "services", "secrets", "namespaces"]
    verbs: ["get", "list", "watch"]

  # Events (for imagepull collector)
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["get", "list", "watch"]

  # Leader election
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "create", "update"]
```

### Secret Access

Some collectors (database, cloudbalance) require access to Secrets:

- Use namespace-based RBAC to limit secret access
- Consider using external secret managers (Vault, External Secrets Operator)
- Rotate credentials regularly
- Monitor secret access via audit logs

### Network Policies

If using network policies, allow:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: sealos-state-metrics
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: sealos-state-metrics
  policyTypes:
    - Ingress
    - Egress
  ingress:
    # Allow Prometheus to scrape metrics
    - from:
        - namespaceSelector:
            matchLabels:
              name: monitoring
      ports:
        - protocol: TCP
          port: 9090
  egress:
    # Allow Kubernetes API access
    - to:
        - namespaceSelector: {}
          podSelector:
            matchLabels:
              component: apiserver
      ports:
        - protocol: TCP
          port: 6443
    # Allow DNS
    - to:
        - namespaceSelector:
            matchLabels:
              name: kube-system
      ports:
        - protocol: UDP
          port: 53
```

## Performance Tuning

### Resource Recommendations

**Small Clusters** (< 50 nodes):
```yaml
resources:
  requests:
    cpu: 50m
    memory: 64Mi
  limits:
    cpu: 500m
    memory: 256Mi
```

**Medium Clusters** (50-200 nodes):
```yaml
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 1000m
    memory: 512Mi
```

**Large Clusters** (> 200 nodes):
```yaml
resources:
  requests:
    cpu: 200m
    memory: 256Mi
  limits:
    cpu: 2000m
    memory: 1Gi
```

### Configuration Tuning

```yaml
# Reduce Kubernetes API load
kubernetes:
  qps: 50       # Increase for large clusters
  burst: 100    # Burst capacity

# Adjust collector intervals
collectors:
  domain:
    checkInterval: "1m"   # Default
    checkTimeout: "30s"   # Default
    dialRetries: 3        # Default

  database:
    checkInterval: "5m"   # Balance between freshness and load
    checkTimeout: "10s"   # Adjust based on network latency

  zombie:
    checkInterval: "30s"  # Frequent for quick detection
```

## Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

### How to Contribute

1. **Report Bugs**: Open an issue with reproduction steps
2. **Suggest Features**: Describe your use case and proposed solution
3. **Submit PRs**: Fork, create a feature branch, and submit a pull request
4. **Add Collectors**: Create new collectors for community benefit
5. **Improve Docs**: Fix typos, add examples, clarify explanations

### Development Workflow

```bash
# Fork and clone
git clone https://github.com/yourusername/sealos-state-metrics.git
cd sealos-state-metrics

# Create feature branch
git checkout -b feature/my-collector

# Make changes and test
make test
make lint

# Commit and push
git commit -m "feat: add my-collector for monitoring X"
git push origin feature/my-collector

# Open pull request
```

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for full text.

## Support

- **Issues**: [GitHub Issues](https://github.com/labring/sealos-state-metrics/issues)
- **Discussions**: [GitHub Discussions](https://github.com/labring/sealos-state-metrics/discussions)
- **Community**: [Sealos Community](https://github.com/labring/sealos)
- **Documentation**: [Full Documentation](docs/)

## Related Projects

- [Sealos](https://github.com/labring/sealos) - Cloud operating system based on Kubernetes
- [kube-state-metrics](https://github.com/kubernetes/kube-state-metrics) - Original inspiration
- [Prometheus](https://prometheus.io/) - Metrics collection and alerting
- [VictoriaMetrics](https://victoriametrics.com/) - Time series database

## Roadmap

### v0.2.0
- [ ] Cardinality control and metrics filtering
- [ ] Plugin discovery and dynamic loading
- [ ] Multi-cluster support

### v0.3.0
- [ ] OpenTelemetry support (OTLP exporter)
- [ ] Built-in recording rules (VMRule integration)
- [ ] Alerting integration (AlertManager direct support)

### v1.0.0
- [ ] Stability guarantees
- [ ] Performance benchmarks
- [ ] Production hardening

---

**Made with ❤️ by the Sealos team**
