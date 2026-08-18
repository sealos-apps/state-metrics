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
	"fmt"
	"reflect"
	"time"

	"github.com/labring/sealos-state-metrics/config-controller/internal/configmerge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	statemetricsv1alpha1 "github.com/labring/sealos-state-metrics/config-controller/api/v1alpha1"
)

// CollectorConfigReconciler reconciles a CollectorConfig object
type CollectorConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	TargetNamespace string
	TargetName      string
	ConfigKey       string
	BaseConfigKey   string
	WatchNamespace  string
}

// +kubebuilder:rbac:groups=state-metrics.sealos.io,resources=collectorconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;update;patch

func (r *CollectorConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	if err := r.validate(); err != nil {
		return ctrl.Result{}, err
	}

	configs := &statemetricsv1alpha1.CollectorConfigList{}
	if err := r.List(ctx, configs, r.listOptions()...); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list collector configs: %w", err)
	}

	configMap := &corev1.ConfigMap{}
	key := client.ObjectKey{
		Namespace: r.TargetNamespace,
		Name:      r.TargetName,
	}
	if err := r.Get(ctx, key, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, fmt.Errorf("target configmap %s/%s not found", key.Namespace, key.Name)
		}

		return ctrl.Result{}, fmt.Errorf("failed to get target configmap %s/%s: %w", key.Namespace, key.Name, err)
	}

	nextData, err := configmerge.ConfigMapData(configMap.Data, configs.Items, configmerge.Options{
		ConfigKey:     r.ConfigKey,
		BaseConfigKey: r.BaseConfigKey,
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	if reflect.DeepEqual(configMap.Data, nextData) {
		logger.V(1).Info("Target ConfigMap already up to date", "configMap", key, "collectorConfigs", len(configs.Items))
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(configMap.DeepCopy())
	configMap.Data = nextData
	if err := r.Patch(ctx, configMap, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch target configmap %s/%s: %w", key.Namespace, key.Name, err)
	}

	logger.Info("Updated target ConfigMap from CollectorConfig resources", "configMap", key, "collectorConfigs", len(configs.Items))

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CollectorConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&statemetricsv1alpha1.CollectorConfig{}).
		Named("collectorconfig").
		Complete(r)
}

func (r *CollectorConfigReconciler) validate() error {
	if r.TargetNamespace == "" {
		return fmt.Errorf("target namespace cannot be empty")
	}

	if r.TargetName == "" {
		return fmt.Errorf("target configmap name cannot be empty")
	}

	return nil
}

func (r *CollectorConfigReconciler) listOptions() []client.ListOption {
	if r.WatchNamespace == "" {
		return nil
	}

	return []client.ListOption{client.InNamespace(r.WatchNamespace)}
}
