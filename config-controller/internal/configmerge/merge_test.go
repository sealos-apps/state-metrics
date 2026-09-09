/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package configmerge

import (
	"testing"

	statemetricsv1alpha1 "github.com/labring/sealos-state-metrics/config-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

func TestConfigMapDataMergesCollectorsAndDeduplicatesLists(t *testing.T) {
	t.Parallel()

	data := map[string]string{
		DefaultConfigKey: `
enabledCollectors:
  - node
  - domain
collectors:
  domain:
    domains:
      - endpoint: example.com
    checkInterval: 1m
`,
	}

	merged, err := ConfigMapData(data, []statemetricsv1alpha1.CollectorConfig{
		newCollectorConfig("monitoring", "domain-extra", "domain", true, `
domains:
  - endpoint: example.com
  - endpoint: api.example.com
checkTimeout: 30s
`),
		newCollectorConfig("monitoring", "registry", "registryproxy", true, `
registries:
  - endpoint: https://registry.example.com
`),
	}, Options{})
	if err != nil {
		t.Fatalf("ConfigMapData() error = %v", err)
	}

	root := unmarshalConfig(t, merged[DefaultConfigKey])
	assertStringList(t, root["enabledCollectors"].([]any), []string{"node", "domain", "registryproxy"})

	collectors := root["collectors"].(map[string]any)
	domain := collectors["domain"].(map[string]any)
	domains := domain["domains"].([]any)
	if len(domains) != 2 {
		t.Fatalf("expected deduplicated domains length 2, got %d: %#v", len(domains), domains)
	}

	if domain["checkInterval"] != "1m" {
		t.Fatalf("expected base checkInterval to be preserved, got %#v", domain["checkInterval"])
	}

	if domain["checkTimeout"] != "30s" {
		t.Fatalf("expected overlay checkTimeout, got %#v", domain["checkTimeout"])
	}

	if _, ok := merged[DefaultBaseConfigKey]; !ok {
		t.Fatalf("expected base config key to be initialized")
	}
}

func TestConfigMapDataUsesBaseConfigSoDeletedCRsDisappear(t *testing.T) {
	t.Parallel()

	base := `
enabledCollectors:
  - node
collectors:
  node: {}
`
	data := map[string]string{
		DefaultConfigKey:     base,
		DefaultBaseConfigKey: base,
	}

	withCR, err := ConfigMapData(data, []statemetricsv1alpha1.CollectorConfig{
		newCollectorConfig("monitoring", "domain-extra", "domain", true, `
domains:
  - endpoint: example.com
`),
	}, Options{})
	if err != nil {
		t.Fatalf("first ConfigMapData() error = %v", err)
	}

	withoutCR, err := ConfigMapData(withCR, nil, Options{})
	if err != nil {
		t.Fatalf("second ConfigMapData() error = %v", err)
	}

	root := unmarshalConfig(t, withoutCR[DefaultConfigKey])
	assertStringList(t, root["enabledCollectors"].([]any), []string{"node"})

	collectors := root["collectors"].(map[string]any)
	if _, exists := collectors["domain"]; exists {
		t.Fatalf("expected deleted CR contribution to be removed, got collectors %#v", collectors)
	}
}

func TestConfigMapDataSkipsDisabledConfigs(t *testing.T) {
	t.Parallel()

	data := map[string]string{
		DefaultConfigKey: `
enabledCollectors: []
collectors: {}
`,
	}

	merged, err := ConfigMapData(data, []statemetricsv1alpha1.CollectorConfig{
		newCollectorConfig("monitoring", "disabled-domain", "domain", false, `
domains:
  - endpoint: example.com
`),
	}, Options{})
	if err != nil {
		t.Fatalf("ConfigMapData() error = %v", err)
	}

	root := unmarshalConfig(t, merged[DefaultConfigKey])
	assertStringList(t, root["enabledCollectors"].([]any), []string{})

	collectors := root["collectors"].(map[string]any)
	if _, exists := collectors["domain"]; exists {
		t.Fatalf("expected disabled CR to be skipped, got collectors %#v", collectors)
	}
}

func TestConfigMapDataRejectsEmptyCollector(t *testing.T) {
	t.Parallel()

	_, err := ConfigMapData(map[string]string{}, []statemetricsv1alpha1.CollectorConfig{
		newCollectorConfig("monitoring", "invalid", "", true, `{}`),
	}, Options{})
	if err == nil {
		t.Fatalf("expected error for empty collector")
	}
}

func newCollectorConfig(namespace, name, collector string, enabled bool, value string) statemetricsv1alpha1.CollectorConfig {
	return statemetricsv1alpha1.CollectorConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: statemetricsv1alpha1.CollectorConfigSpec{
			Collector: collector,
			Enabled:   &enabled,
			Value: runtime.RawExtension{
				Raw: []byte(value),
			},
		},
	}
}

func unmarshalConfig(t *testing.T, content string) map[string]any {
	t.Helper()

	var root map[string]any
	if err := yaml.Unmarshal([]byte(content), &root); err != nil {
		t.Fatalf("failed to unmarshal config: %v\n%s", err, content)
	}

	return root
}

func assertStringList(t *testing.T, got []any, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("list length mismatch: got %#v, want %#v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("list[%d] = %#v, want %#v; full list %#v", i, got[i], want[i], got)
		}
	}
}
