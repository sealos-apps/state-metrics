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
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	statemetricsv1alpha1 "github.com/labring/sealos-state-metrics/config-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

const DefaultConfigKey = "config.yaml"
const DefaultBaseConfigKey = "base-config.yaml"

type Options struct {
	ConfigKey     string
	BaseConfigKey string
}

// ConfigMapData returns ConfigMap data with CollectorConfig contributions
// merged into the normal sealos-state-metrics config file.
func ConfigMapData(data map[string]string, configs []statemetricsv1alpha1.CollectorConfig, opts Options) (map[string]string, error) {
	configDataKey := valueOrDefault(opts.ConfigKey, DefaultConfigKey)
	baseConfigKey := valueOrDefault(opts.BaseConfigKey, DefaultBaseConfigKey)

	nextData := make(map[string]string, len(data)+1)
	for key, value := range data {
		nextData[key] = value
	}

	root := map[string]any{}
	baseContent := data[baseConfigKey]
	if strings.TrimSpace(baseContent) == "" {
		baseContent = data[configDataKey]
		nextData[baseConfigKey] = baseContent
	}

	if raw := strings.TrimSpace(baseContent); raw != "" {
		if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
			return nil, fmt.Errorf("failed to unmarshal base config: %w", err)
		}
	}

	collectors, ok := root["collectors"].(map[string]any)
	if !ok {
		collectors = map[string]any{}
		root["collectors"] = collectors
	}

	enabledCollectors := toStringSlice(root["enabledCollectors"])

	sort.SliceStable(configs, func(i, j int) bool {
		return keyForConfig(configs[i]) < keyForConfig(configs[j])
	})

	for i := range configs {
		cfg := configs[i]
		if !isEnabled(cfg) {
			continue
		}

		collectorName := strings.TrimSpace(cfg.Spec.Collector)
		if collectorName == "" {
			return nil, fmt.Errorf("collector config %s/%s has empty spec.collector", cfg.Namespace, cfg.Name)
		}

		value, err := decodeRawValue(cfg.Spec.Value)
		if err != nil {
			return nil, fmt.Errorf("collector config %s/%s has invalid spec.value: %w", cfg.Namespace, cfg.Name, err)
		}

		collectors[collectorName], err = mergeValues(collectors[collectorName], value)
		if err != nil {
			return nil, fmt.Errorf("failed to merge collector %q from %s/%s: %w", collectorName, cfg.Namespace, cfg.Name, err)
		}

		enabledCollectors = appendUniqueString(enabledCollectors, collectorName)
	}

	root["enabledCollectors"] = enabledCollectors

	rendered, err := yaml.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged config: %w", err)
	}

	nextData[configDataKey] = string(rendered)

	return nextData, nil
}

func isEnabled(cfg statemetricsv1alpha1.CollectorConfig) bool {
	return cfg.Spec.Enabled == nil || *cfg.Spec.Enabled
}

func decodeRawValue(raw runtime.RawExtension) (any, error) {
	if len(raw.Raw) == 0 {
		return map[string]any{}, nil
	}

	var value any
	if err := yaml.Unmarshal(raw.Raw, &value); err != nil {
		return nil, err
	}

	if value == nil {
		return map[string]any{}, nil
	}

	return normalizeYAML(value), nil
}

func normalizeYAML(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = normalizeYAML(item)
		}

		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalizeYAML(item)
		}

		return out
	default:
		return v
	}
}

func mergeValues(base, overlay any) (any, error) {
	if base == nil {
		return overlay, nil
	}

	switch overlayValue := overlay.(type) {
	case map[string]any:
		baseMap, ok := base.(map[string]any)
		if !ok {
			return overlayValue, nil
		}

		out := make(map[string]any, len(baseMap)+len(overlayValue))
		for key, value := range baseMap {
			out[key] = value
		}

		for key, overlayItem := range overlayValue {
			merged, err := mergeValues(out[key], overlayItem)
			if err != nil {
				return nil, err
			}

			out[key] = merged
		}

		return out, nil
	case []any:
		baseList, ok := base.([]any)
		if !ok {
			return overlayValue, nil
		}

		return mergeLists(baseList, overlayValue)
	default:
		return overlayValue, nil
	}
}

func mergeLists(base, overlay []any) ([]any, error) {
	out := make([]any, 0, len(base)+len(overlay))
	seen := map[string]struct{}{}

	for _, item := range append(base, overlay...) {
		key, err := stableKey(item)
		if err != nil {
			return nil, err
		}

		if _, exists := seen[key]; exists {
			continue
		}

		seen[key] = struct{}{}
		out = append(out, item)
	}

	return out, nil
}

func stableKey(value any) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(value); err != nil {
		return "", err
	}

	return strings.TrimSpace(buf.String()), nil
}

func toStringSlice(value any) []string {
	switch v := value.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = appendUniqueString(out, s)
			}
		}

		return out
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = appendUniqueString(out, item)
		}

		return out
	default:
		return []string{}
	}
}

func appendUniqueString(items []string, item string) []string {
	item = strings.TrimSpace(item)
	if item == "" {
		return items
	}

	for _, existing := range items {
		if existing == item {
			return items
		}
	}

	return append(items, item)
}

func keyForConfig(cfg statemetricsv1alpha1.CollectorConfig) string {
	return cfg.Namespace + "/" + cfg.Name
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}
