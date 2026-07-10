package userbalance

import (
	"context"
	"fmt"

	"github.com/labring/sealos-state-metrics/pkg/collector"
	"github.com/labring/sealos-state-metrics/pkg/collector/base"
	"github.com/labring/sealos-state-metrics/pkg/database"
	"github.com/labring/sealos-state-metrics/pkg/registry"
)

const collectorName = "userbalance"

func init() {
	registry.MustRegister(collectorName, NewCollector)
}

// NewCollector creates a new UserBalance collector
func NewCollector(factoryCtx *collector.FactoryContext) (collector.Collector, error) {
	// 1. Start with hard-coded defaults
	cfg := NewDefaultConfig()

	// 2. Load configuration from ConfigLoader pipe (file -> env)
	// ConfigLoader is never nil and handles priority: defaults < file < env
	if err := factoryCtx.ConfigLoader.LoadModuleConfig("collectors.userbalance", cfg); err != nil {
		factoryCtx.Logger.WithError(err).
			Debug("Failed to load userbalance collector config, using defaults")
	}

	c := &Collector{
		BaseCollector: base.NewBaseCollector(
			collectorName,
			factoryCtx.Logger,
			base.WithWaitReadyOnCollect(true),
		),
		config:   cfg,
		balances: make(map[string]balanceSample),
		logger:   factoryCtx.Logger,
	}

	c.initMetrics(factoryCtx.MetricsNamespace)

	// Set lifecycle hooks
	c.SetLifecycle(base.LifecycleFuncs{
		StartFunc: func(ctx context.Context) error {
			pgClient, err := database.InitPgClient(
				ctx,
				cfg.DatabaseConfig.DSN,
				database.WithMinConns(1),
			)
			if err != nil {
				factoryCtx.Logger.WithError(err).
					Debug("Failed to load postgres client")
				return fmt.Errorf("failed to initialize postgres client: %w", err)
			}

			c.pgClient = pgClient
			if cfg.DatabaseConfig.LocalDSN != "" {
				localPgClient, localErr := database.InitPgClient(
					ctx,
					cfg.DatabaseConfig.LocalDSN,
					database.WithMinConns(1),
				)
				if localErr != nil {
					factoryCtx.Logger.WithError(localErr).
						Warn("Failed to initialize local postgres client; discovered users will be skipped")
				} else {
					c.localPgClient = localPgClient
				}
			}

			// Start background polling
			go c.pollLoop(ctx)

			c.logger.Info("UserBalance collector started successfully")

			return nil
		},
		CollectFunc: c.collect,
		StopFunc: func() error {
			if c.pgClient != nil {
				c.pgClient.Close()
			}

			if c.localPgClient != nil {
				c.localPgClient.Close()
			}

			c.logger.Debug("Database connections closed")

			c.pgClient = nil
			c.localPgClient = nil

			return nil
		},
	})

	return c, nil
}
