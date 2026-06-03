# sealos-state-metrics Helm Chart

Kubernetes state metrics collector for Sealos monitoring.

## Prerequisites

- Kubernetes 1.19+
- Helm 3.0+
- Prometheus Operator (optional, for ServiceMonitor)

## Installing the Chart

To install the chart with the release name `sealos-state-metrics`:

```bash
helm install sealos-state-metrics ./deploy/charts/sealos-state-metrics
```

To install as a `Deployment` with multiple replicas:

```bash
helm install sealos-state-metrics ./deploy/charts/sealos-state-metrics \
  --set deploymentMode=deployment \
  --set replicaCount=3
```

To install in a specific namespace:

```bash
helm install sealos-state-metrics ./deploy/charts/sealos-state-metrics -n sealos-system --create-namespace
```

## Uninstalling the Chart

```bash
helm uninstall sealos-state-metrics
```

## Configuration

The following table lists the configurable parameters of the chart and their default values.

### Global Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `deploymentMode` | Workload type: `daemonset` or `deployment` | `daemonset` |
| `replicaCount` | Number of replicas | `1` |
| `image` | Image reference | `ghcr.io/labring/sealos-state-metrics:latest` |
| `imagePullPolicy` | Image pull policy | `IfNotPresent` |
| `updateStrategy.maxUnavailable` | Maximum unavailable pods during rolling update | `25%` |

### Scheduling

| Parameter | Description | Default |
|-----------|-------------|---------|
| `scheduling.preferDifferentNodes.enabled` | Add soft pod anti-affinity in `deployment` mode | `true` |
| `scheduling.preferDifferentNodes.topologyKey` | Topology key used for pod spreading preference | `kubernetes.io/hostname` |
| `scheduling.preferDifferentNodes.weight` | Weight for the preferred anti-affinity rule | `100` |

### Service Account

| Parameter | Description | Default |
|-----------|-------------|---------|
| `serviceAccount.create` | Create service account | `true` |
| `serviceAccount.name` | Service account name | `""` |
| `serviceAccount.annotations` | Service account annotations | `{}` |

### RBAC

| Parameter | Description | Default |
|-----------|-------------|---------|
| `rbac.create` | Create RBAC resources | `true` |

### Service

| Parameter | Description | Default |
|-----------|-------------|---------|
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service port | `9090` |
| `service.targetPort` | Container port | `9090` |

### ServiceMonitor (Prometheus Operator)

| Parameter | Description | Default |
|-----------|-------------|---------|
| `serviceMonitor.enabled` | Create ServiceMonitor | `false` |
| `serviceMonitor.interval` | Scrape interval | `30s` |
| `serviceMonitor.scrapeTimeout` | Scrape timeout | `10s` |

### Application Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `config.logLevel` | Log level | `info` |
| `config.logFormat` | Log format (json/text) | `text` |
| `config.debug` | Enable debug mode | `false` |
| `config.metricsPath` | Metrics endpoint path | `/metrics` |
| `config.healthPath` | Health endpoint path | `/health` |
| `config.metricsNamespace` | Metrics namespace prefix | `sealos` |

### Leader Election

| Parameter | Description | Default |
|-----------|-------------|---------|
| `config.leaderElection.enabled` | Enable leader election | `true` |
| `config.leaderElection.namespace` | Leader election namespace | `""` (uses release namespace) |
| `config.leaderElection.leaseName` | Lease name | `sealos-state-metrics` |
| `config.leaderElection.leaseDuration` | Lease duration | `15s` |
| `config.leaderElection.renewDeadline` | Renew deadline | `10s` |
| `config.leaderElection.retryPeriod` | Retry period | `2s` |

### Collectors

| Parameter | Description | Default |
|-----------|-------------|---------|
| `config.enabledCollectors` | List of enabled collectors | `["domain","node","pod","imagepull","zombie"]` |

#### Domain Collector

| Parameter | Description | Default |
|-----------|-------------|---------|
| `collectors.domain.domains` | List of domains to check. Supports mixed string entries and object entries with `endpoint` and `skipTLSVerify` | `[]` |
| `collectors.domain.checkTimeout` | HTTP and TLS check timeout | `30s` |
| `collectors.domain.checkInterval` | Check interval | `1m` |
| `collectors.domain.dialRetries` | Number of connection attempts for HTTP and certificate checks. HTTPS attempts include both TCP dial and TLS handshake | `3` |
| `collectors.domain.includeIPv4` | Include IPv4 addresses returned by DNS | `true` |
| `collectors.domain.includeIPv6` | Include IPv6 addresses returned by DNS | `true` |
| `collectors.domain.includeCertCheck` | Include certificate checks | `true` |
| `collectors.domain.includeHTTPCheck` | Include HTTP checks | `true` |

Notes:

- String entry example: `example.com`
- Object entry example: `{ endpoint: internal.example.local:8443, skipTLSVerify: true }`
- `includeIPv4` and `includeIPv6` are global Domain collector switches, not per-domain options
- `skipTLSVerify` applies to both HTTPS checks and certificate checks for that domain
- The env var `COLLECTORS_DOMAIN_DOMAINS` only supports legacy comma-separated string entries and overrides YAML `domains`

#### Node Collector

| Parameter | Description | Default |
|-----------|-------------|---------|
| `config.node.enabled` | Enable node collector | `true` |

#### Pod Collector

| Parameter | Description | Default |
|-----------|-------------|---------|
| `config.pod.enabled` | Enable pod collector | `true` |
| `config.pod.restartThreshold` | Restart count threshold | `5` |

#### Image Pull Collector

| Parameter | Description | Default |
|-----------|-------------|---------|
| `config.imagepull.enabled` | Enable image pull collector | `true` |
| `config.imagepull.slowPullThreshold` | Slow pull threshold | `5m` |

#### Zombie Collector

| Parameter | Description | Default |
|-----------|-------------|---------|
| `config.zombie.enabled` | Enable zombie collector | `true` |
| `config.zombie.checkInterval` | Check interval | `30s` |

### Resources

| Parameter | Description | Default |
|-----------|-------------|---------|
| `resources.requests.cpu` | CPU request | `100m` |
| `resources.requests.memory` | Memory request | `128Mi` |
| `resources.limits.cpu` | CPU limit | `1000m` |
| `resources.limits.memory` | Memory limit | `512Mi` |

## Example: Custom Configuration

```yaml
# values-custom.yaml
replicaCount: 2

config:
  logging:
    level: debug

collectors:
  domain:
    domains:
      - example.com
      - endpoint: internal.example.local:8443
        skipTLSVerify: true
        path: /healthz?ready=true
        method: GET
        headers:
          X-Probe: sealos-state-metrics
        expectedStatusCodes: [200, 204]
      - test.com
    checkTimeout: 30s
    checkInterval: 1m
    dialRetries: 3
    includeIPv4: true
    includeIPv6: true

leaderElection:
  enabled: true
  namespace: sealos-system

serviceMonitor:
  enabled: true
  interval: 1m

resources:
  requests:
    cpu: 200m
    memory: 256Mi
  limits:
    cpu: 2000m
    memory: 1Gi
```

Install with custom values:

```bash
helm install sealos-state-metrics ./deploy/charts/sealos-state-metrics -f values-custom.yaml
```

## Metrics

The following metrics are exposed:

- `sealos_domain_status` - Domain health check status
- `sealos_node_*` - Node metrics
- `sealos_pod_*` - Pod metrics
- `sealos_imagepull_*` - Image pull metrics
- `sealos_zombie_*` - Zombie process metrics

## Troubleshooting

### Check pod status

```bash
kubectl get pods -l app.kubernetes.io/name=sealos-state-metrics
```

### View logs

```bash
kubectl logs -l app.kubernetes.io/name=sealos-state-metrics -f
```

### Test metrics endpoint

```bash
kubectl port-forward svc/sealos-state-metrics 9090:9090
curl http://localhost:9090/metrics
```

### Verify RBAC permissions

```bash
kubectl auth can-i get nodes --as=system:serviceaccount:default:sealos-state-metrics
kubectl auth can-i get pods --as=system:serviceaccount:default:sealos-state-metrics
```
