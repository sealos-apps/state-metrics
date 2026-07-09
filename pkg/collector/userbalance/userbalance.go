package userbalance

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labring/sealos-state-metrics/pkg/collector/base"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
)

// Collector implements user balance monitoring
type Collector struct {
	*base.BaseCollector
	config   *Config
	logger   *log.Entry
	pgClient *pgxpool.Pool

	// Prometheus metrics
	balanceGauge *prometheus.Desc

	// Internal state
	mu       sync.RWMutex
	balances map[string]float64
}

// initMetrics initializes Prometheus metric descriptors
func (c *Collector) initMetrics(namespace string) {
	c.balanceGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "userbalance", "balance"),
		"Current balance for each sealos user",
		[]string{"region", "uuid", "uid", "owner", "type", "level"},
		nil,
	)

	// Register descriptor
	c.MustRegisterDesc(c.balanceGauge)
}

// HasSynced returns true (polling collector is always synced)
func (c *Collector) HasSynced() bool {
	return true
}

// Interval returns the polling interval
func (c *Collector) Interval() time.Duration {
	return c.config.CheckInterval
}

// pollLoop periodically queries user balances
func (c *Collector) pollLoop(ctx context.Context) {
	c.RunPollLoop(ctx, c.Poll, base.PollLoopOptions{
		Interval:  c.config.CheckInterval,
		Operation: "userbalance",
	})
}

// Poll queries all configured user accounts
func (c *Collector) Poll(ctx context.Context) error {
	if len(c.config.UserConfig) == 0 {
		c.logger.Debug("No sealos user configured for monitoring")
		return nil
	}

	c.logger.WithField("count", len(c.config.UserConfig)).Info("Starting cloud balance checks")

	newBalances := make(map[string]float64)
	for _, user := range c.config.UserConfig {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		balance, err := c.QueryBalance(user)
		if err != nil {
			c.logger.WithFields(log.Fields{
				"user_id": user.UID,
			}).WithError(err).Error("Failed to query sealos user balance")

			continue
		}

		key := user.Region + ":" + user.UID
		newBalances[key] = balance

		c.logger.WithFields(log.Fields{
			"region":  user.Region,
			"uid":     user.UID,
			"balance": balance,
		}).Debug("User balance updated")
	}

	c.mu.Lock()
	c.balances = newBalances
	c.mu.Unlock()

	return nil
}

// collect implements the collect method for Prometheus
func (c *Collector) collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, user := range c.config.UserConfig {
		key := user.Region + ":" + user.UID

		balance, exists := c.balances[key]
		if !exists {
			continue
		}

		ch <- prometheus.MustNewConstMetric(
			c.balanceGauge,
			prometheus.GaugeValue,
			balance,
			user.Region,
			user.UUID,
			user.UID,
			user.Owner,
			user.Type,
			user.Level,
		)

		user.Type = "quota"
		ch <- prometheus.MustNewConstMetric(
			c.balanceGauge,
			prometheus.GaugeValue,
			user.Quota,
			user.Region,
			user.UUID,
			user.UID,
			user.Owner,
			user.Type,
			user.Level,
		)
	}
}
