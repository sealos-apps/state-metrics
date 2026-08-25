package crds

import (
	"context"
	"errors"
	"fmt"

	"github.com/labring/sealos-state-metrics/pkg/collector"
	"github.com/labring/sealos-state-metrics/pkg/collector/base"
	"github.com/labring/sealos-state-metrics/pkg/collector/crds/informer"
	"github.com/labring/sealos-state-metrics/pkg/registry"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const collectorName = "crds"

func init() {
	registry.MustRegister(collectorName, NewCollector)
}

// NewCollector creates a new UserBalance collector
func NewCollector(factoryCtx *collector.FactoryContext) (collector.Collector, error) {
	// 1. Start with hard-coded defaults
	cfg := NewDefaultConfig()

	// 2. Load configuration from ConfigLoader pipe (file -> env)
	// ConfigLoader is never nil and handles priority: defaults < file < env
	if err := factoryCtx.ConfigLoader.LoadModuleConfig("collectors.crds", cfg); err != nil {
		factoryCtx.Logger.WithError(err).
			Debug("Failed to load userbalance c config, using defaults")
	}

	// 3. Check if any CRDs configured (no config = disabled)
	if len(cfg.CRDs) == 0 {
		factoryCtx.Logger.Debug("No CRDs configured for dynamic c, skipping")
		return nil, nil
	}

	// 4. Get Kubernetes rest config (lazy initialization)
	restConfig, err := factoryCtx.GetRestConfig()
	if err != nil {
		return nil, fmt.Errorf("kubernetes rest config is required but not available: %w", err)
	}

	// 5. Create dynamic client
	dynamicClient, err := createDynamicClient(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	// 6. Create a c for each CRD
	c := &Collector{
		BaseCollector: base.NewBaseCollector(
			collectorName,
			factoryCtx.Logger,
			base.WithWaitReadyOnCollect(true),
		),
		crdCollectors: make([]*CrdCollector, 0, len(cfg.CRDs)),
		logger:        factoryCtx.Logger,
	}
	for i := range cfg.CRDs {
		crdCfg := &cfg.CRDs[i]

		// Validate CRD config
		if crdCfg.Name == "" {
			return nil, fmt.Errorf("CRD config %d: name is required", i)
		}

		if crdCfg.GVR.Resource == "" {
			return nil, fmt.Errorf("CRD config %s: gvr.resource is required", crdCfg.Name)
		}

		metricStore, err := newMetricStore(
			crdCfg,
			"",
			factoryCtx.Logger.WithField("crd", crdCfg.Name),
		)
		if err != nil {
			return nil, fmt.Errorf(
				"failed to initialize metric store for CRD %s: %w",
				crdCfg.Name,
				err,
			)
		}

		informerConfig := informer.InformerConfig{
			GVR: schema.GroupVersionResource{
				Group:    crdCfg.GVR.Group,
				Resource: crdCfg.GVR.Resource,
				Version:  crdCfg.GVR.Version,
			},
			ResyncPeriod:   crdCfg.ResyncPeriod,
			Namespaces:     crdCfg.Namespaces,
			TransformPaths: metricStore.RequiredPaths(),
		}

		i, err := informer.NewInformer(
			dynamicClient,
			&informerConfig,
			metricStore,
			factoryCtx.Logger.WithField("crd", crdCfg.Name),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize i for CRD %s: %w", crdCfg.Name, err)
		}

		crdCollector, err := NewCrdCollector(
			crdCfg,
			metricStore,
			i,
			"",
			factoryCtx.Logger.WithField("crd", crdCfg.Name),
		)
		if err != nil {
			return nil, err
		}

		c.crdCollectors = append(c.crdCollectors, crdCollector)
	}

	factoryCtx.Logger.WithField("count", len(c.crdCollectors)).Info("Created crd collectors")

	// 7. Init all crd collector Metrics
	c.initMetrics()

	// 8. Set lifecycle hooks
	c.SetLifecycle(base.LifecycleFuncs{
		StartFunc: func(ctx context.Context) error {
			syncFuncs := make([]cache.InformerSynced, 0, len(c.crdCollectors))
			startedCollectors := make([]*CrdCollector, 0, len(c.crdCollectors))
			stopStartedInformers := func() {
				for _, crdCollector := range startedCollectors {
					if err := crdCollector.informer.Stop(); err != nil {
						c.logger.WithError(err).Warn("Failed to stop CRD informer")
					}
				}
			}

			// Start all informers first so initial cache sync can run in parallel.
			for _, crdCollector := range c.crdCollectors {
				if err := crdCollector.informer.Start(); err != nil {
					stopStartedInformers()

					return err
				}

				startedCollectors = append(startedCollectors, crdCollector)
				syncFuncs = append(syncFuncs, crdCollector.informer.HasSynced)
			}

			c.logger.Info("Waiting for CRD informer caches to sync")

			if !cache.WaitForCacheSync(ctx.Done(), syncFuncs...) {
				stopStartedInformers()

				return errors.New("failed to sync CRD informer caches")
			}

			c.SetReady()

			return nil
		},
		CollectFunc: c.collect,
		StopFunc: func() error {
			// Stop all crdCollector informer
			for _, crdCollector := range c.crdCollectors {
				err := crdCollector.informer.Stop()
				if err != nil {
					return err
				}
			}

			return nil
		},
	})

	return c, nil
}

// createDynamicClient creates a dynamic Kubernetes client
func createDynamicClient(restConfig *rest.Config) (dynamic.Interface, error) {
	if restConfig == nil {
		return nil, errors.New("rest config cannot be nil")
	}

	client, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return client, nil
}

func (c *Collector) initMetrics() {
	for _, crdCollector := range c.crdCollectors {
		for _, desc := range crdCollector.descriptors {
			c.MustRegisterDesc(desc)
		}
	}
}
