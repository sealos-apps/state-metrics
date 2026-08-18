# sealos-state-metrics-config-controller

Standalone Kubebuilder controller for CRD-driven `sealos-state-metrics`
configuration.

The controller watches `CollectorConfig` custom resources and rewrites a target
ConfigMap into the existing `config.yaml` format consumed by the
`sealos-state-metrics` metrics process. The metrics process stays unchanged and
continues to reload configuration from the mounted ConfigMap.

## API

```yaml
apiVersion: state-metrics.sealos.io/v1alpha1
kind: CollectorConfig
metadata:
  name: domain-example
  namespace: monitoring
spec:
  collector: domain
  enabled: true
  value:
    domains:
      - endpoint: example.com
    checkTimeout: 30s
```

## Controller Flags

- `--target-namespace`: namespace containing the target ConfigMap
- `--target-configmap-name`: target ConfigMap name
- `--watch-namespace`: namespace to watch for `CollectorConfig` resources; empty watches all namespaces
- `--config-key`: generated config key, default `config.yaml`
- `--base-config-key`: baseline config key, default `base-config.yaml`
- `--leader-elect`: enable controller-runtime leader election

## Development

```bash
make generate manifests
make test
make build
```

Build and push:

```bash
make docker-build docker-push IMG=ghcr.io/labring/sealos-state-metrics-config-controller:tag
```

Generate an installer:

```bash
make build-installer IMG=ghcr.io/labring/sealos-state-metrics-config-controller:tag
```
