package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/labring/sealos-state-metrics/pkg/collector"
	"github.com/labring/sealos-state-metrics/pkg/collector/base"
	"github.com/labring/sealos-state-metrics/pkg/registry"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const collectorName = "billing"

func init() {
	registry.MustRegister(collectorName, NewCollector)
}

// NewCollector creates a Sealos billing collector.
func NewCollector(factoryCtx *collector.FactoryContext) (collector.Collector, error) {
	cfg := NewDefaultConfig()
	if err := factoryCtx.ConfigLoader.LoadModuleConfig("collectors.billing", cfg); err != nil {
		factoryCtx.Logger.WithError(err).
			Debug("Failed to load billing collector config, using defaults")
	}

	c := &Collector{
		BaseCollector: base.NewBaseCollector(
			collectorName,
			factoryCtx.Logger,
			base.WithWaitReadyOnCollect(true),
		),
		config: cfg,
		logger: factoryCtx.Logger,
	}
	c.initMetrics(factoryCtx.MetricsNamespace)

	c.SetLifecycle(base.LifecycleFuncs{
		StartFunc: func(ctx context.Context) error {
			if cfg.Mongo.URI == "" {
				return errors.New("mongo uri is required")
			}

			clientOptions := options.Client().
				ApplyURI(cfg.Mongo.URI).
				SetConnectTimeout(cfg.QueryTimeout).
				SetServerSelectionTimeout(cfg.QueryTimeout).
				SetSocketTimeout(cfg.QueryTimeout).
				SetMaxPoolSize(2)

			client, err := mongo.Connect(ctx, clientOptions)
			if err != nil {
				return fmt.Errorf("connect mongo: %w", err)
			}

			if err := client.Ping(ctx, nil); err != nil {
				_ = client.Disconnect(context.Background())
				return fmt.Errorf("ping mongo: %w", err)
			}

			c.mongoClient = client
			go c.pollLoop(ctx)

			c.logger.Info("Billing collector started successfully")

			return nil
		},
		CollectFunc: c.collect,
		StopFunc: func() error {
			if c.mongoClient != nil {
				if err := c.mongoClient.Disconnect(context.Background()); err != nil {
					return fmt.Errorf("disconnect mongo: %w", err)
				}

				c.mongoClient = nil
			}

			c.logger.Info("Billing collector stopped")

			return nil
		},
	})

	return c, nil
}
