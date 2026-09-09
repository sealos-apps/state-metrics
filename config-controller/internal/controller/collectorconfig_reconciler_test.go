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

package controller

import (
	"context"
	"testing"

	statemetricsv1alpha1 "github.com/labring/sealos-state-metrics/config-controller/api/v1alpha1"
	"github.com/labring/sealos-state-metrics/config-controller/internal/configmerge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

func TestCollectorConfigReconcilerUpdatesTargetConfigMap(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := statemetricsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add api scheme: %v", err)
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "monitoring",
					Name:      "sealos-state-metrics",
				},
				Data: map[string]string{
					configmerge.DefaultConfigKey: `
enabledCollectors:
  - node
collectors:
  node: {}
`,
				},
			},
			newCollectorConfig("monitoring", "domain", "domain", `{"domains":[{"endpoint":"example.com"}]}`),
		).
		Build()

	reconciler := &CollectorConfigReconciler{
		Client:          k8sClient,
		Scheme:          scheme,
		TargetNamespace: "monitoring",
		TargetName:      "sealos-state-metrics",
	}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	configMap := &corev1.ConfigMap{}
	if err := k8sClient.Get(
		context.Background(),
		client.ObjectKey{Namespace: "monitoring", Name: "sealos-state-metrics"},
		configMap,
	); err != nil {
		t.Fatalf("failed to get configmap: %v", err)
	}

	root := unmarshalConfig(t, configMap.Data[configmerge.DefaultConfigKey])
	enabled := root["enabledCollectors"].([]any)
	if len(enabled) != 2 || enabled[0] != "node" || enabled[1] != "domain" {
		t.Fatalf("unexpected enabledCollectors: %#v", enabled)
	}

	collectors := root["collectors"].(map[string]any)
	if _, exists := collectors["domain"]; !exists {
		t.Fatalf("expected domain collector config, got %#v", collectors)
	}

	if configMap.Data[configmerge.DefaultBaseConfigKey] == "" {
		t.Fatalf("expected base config key to be stored")
	}
}

func newCollectorConfig(namespace, name, collector, value string) *statemetricsv1alpha1.CollectorConfig {
	return &statemetricsv1alpha1.CollectorConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: statemetricsv1alpha1.CollectorConfigSpec{
			Collector: collector,
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
