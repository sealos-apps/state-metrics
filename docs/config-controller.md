# Config Controller

The config controller lets external systems configure collectors with Kubernetes
custom resources instead of editing the full `config.yaml` ConfigMap.

The controller is a standalone Kubebuilder project under `config-controller/`.
It is deployed as a separate manager Deployment and is not linked into the
`sealos-state-metrics` metrics binary.

It follows the same operational model as a VictoriaMetrics controller and agent:

1. Users create or update `CollectorConfig` resources.
2. The controller watches those resources.
3. The controller rewrites the target ConfigMap into the normal
   `config.yaml` format already consumed by `sealos-state-metrics`.
4. The existing file watcher reloads the mounted ConfigMap and restarts
   collectors with the merged configuration.

## Enabling

The controller is disabled by default.

```yaml
configController:
  enabled: true
  image: ghcr.io/labring/sealos-state-metrics-config-controller:latest
  # Empty means the release namespace.
  namespace: ""
  # Empty means watch CollectorConfig objects in all namespaces.
  watchNamespace: ""
  # Empty means the chart fullname.
  configMapName: ""
```

Install or upgrade the chart with those values. The chart renders:

- `collectorconfigs.state-metrics.sealos.io` CRD
- RBAC for watching `CollectorConfig`
- RBAC for updating the target ConfigMap
- a standalone config controller Deployment
- `base-config.yaml` and `config.yaml` entries in the ConfigMap

`base-config.yaml` is the immutable baseline generated from Helm values. The
controller always starts from that baseline, then applies all enabled
`CollectorConfig` objects. This is what makes deletes deterministic: when a CR is
deleted, its contribution disappears from the next generated `config.yaml`.

When `configController.leaderElection` is true, controller-runtime leader
election ensures only one manager instance reconciles ConfigMaps. This prevents
multi-replica controller Deployments from writing the same ConfigMap at the same
time.

## CRD

`CollectorConfig` is intentionally generic.

```yaml
apiVersion: state-metrics.sealos.io/v1alpha1
kind: CollectorConfig
metadata:
  name: example-domain
  namespace: monitoring
spec:
  collector: domain
  enabled: true
  value:
    domains:
      - endpoint: example.com
      - endpoint: api.example.com
    checkTimeout: 30s
```

The generated ConfigMap contains:

```yaml
enabledCollectors:
  - domain
collectors:
  domain:
    domains:
      - endpoint: example.com
      - endpoint: api.example.com
    checkTimeout: 30s
```

## Merge Rules

- `spec.collector` is the collector name under `collectors.<name>`.
- Each enabled CR automatically adds its collector name to `enabledCollectors`.
- Map fields are merged recursively.
- List fields are appended and deduplicated by item content.
- Scalar fields from CRs override the same field from the baseline.
- CRs are processed in stable `namespace/name` order.
- `spec.enabled: false` keeps the CR object but removes its contribution.
- Deleting a CR removes its contribution because generation starts from
  `base-config.yaml` every time.

For example, two domain CRs can contribute different `domains` entries, while a
baseline `checkInterval` from Helm is preserved.

## Examples

Registry proxy:

```yaml
apiVersion: state-metrics.sealos.io/v1alpha1
kind: CollectorConfig
metadata:
  name: registry-proxy-public
  namespace: monitoring
spec:
  collector: registryproxy
  value:
    registries:
      - endpoint: http://registry-proxy.example.com:5000
        repository: library/busybox
        reference: latest
    checkTimeout: 30s
    checkInterval: 1m
```

Cloud balance:

```yaml
apiVersion: state-metrics.sealos.io/v1alpha1
kind: CollectorConfig
metadata:
  name: cloud-balance-prod
  namespace: monitoring
spec:
  collector: cloudbalance
  value:
    checkInterval: 5m
    accounts:
      - provider: alicloud
        accountId: prod
        accessKeyId: ${ACCESS_KEY_ID}
        accessKeySecret: ${ACCESS_KEY_SECRET}
        regionId: cn-hangzhou
```

CRDs collector:

```yaml
apiVersion: state-metrics.sealos.io/v1alpha1
kind: CollectorConfig
metadata:
  name: kubeblocks-clusters
  namespace: monitoring
spec:
  collector: crds
  value:
    crds:
      - name: kubeblocks-cluster
        gvr:
          group: apps.kubeblocks.io
          version: v1alpha1
          resource: clusters
        commonLabels:
          namespace: metadata.namespace
          name: metadata.name
        metrics:
          - type: string_state
            name: kubeblocks_cluster_status
            help: KubeBlocks cluster status
            path: status.phase
```

## Notes

The controller updates only the target ConfigMap. It does not validate
collector-specific semantics beyond checking that `spec.collector` is non-empty
and `spec.value` is valid YAML/JSON-compatible data. Invalid collector settings
will be reported by the normal collector initialization path during reload.

Standalone development commands:

```bash
cd config-controller
make generate manifests
make test
make build
```
