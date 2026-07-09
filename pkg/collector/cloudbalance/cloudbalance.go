package cloudbalance

import (
	"context"
	"sync"
	"time"

	"github.com/labring/sealos-state-metrics/pkg/collector/base"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
)

// Collector implements cloud balance monitoring
type Collector struct {
	*base.BaseCollector
	config *Config
	logger *log.Entry

	// Prometheus metrics
	balanceGauge *prometheus.Desc

	// Internal state
	mu       sync.RWMutex
	balances map[string]float64 // key: provider:accountID
}

// initMetrics initializes Prometheus metric descriptors
func (c *Collector) initMetrics(namespace string) {
	c.balanceGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "cloudbalance", "balance"),
		"Current balance for each cloud account",
		[]string{"provider", "account_id"},
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

// pollLoop periodically queries cloud balances
func (c *Collector) pollLoop(ctx context.Context) {
	c.RunPollLoop(ctx, c.Poll, base.PollLoopOptions{
		Interval:  c.config.CheckInterval,
		Operation: "cloudbalance",
	})
}

// Poll queries all configured cloud accounts
func (c *Collector) Poll(ctx context.Context) error {
	if len(c.config.Accounts) == 0 {
		c.logger.Debug("No cloud accounts configured for monitoring")
		return nil
	}

	c.logger.WithField("count", len(c.config.Accounts)).Info("Starting cloud balance checks")

	newBalances := make(map[string]float64)
	for _, account := range c.config.Accounts {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		balance, err := QueryBalance(account)
		if err != nil {
			c.logger.WithFields(log.Fields{
				"provider":   account.Provider,
				"account_id": account.AccountID,
			}).WithError(err).Error("Failed to query cloud balance")

			continue
		}

		key := string(account.Provider) + ":" + account.AccountID
		newBalances[key] = balance

		c.logger.WithFields(log.Fields{
			"provider":   account.Provider,
			"account_id": account.AccountID,
			"balance":    balance,
		}).Debug("Cloud balance updated")
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

	for _, account := range c.config.Accounts {
		key := string(account.Provider) + ":" + account.AccountID

		balance, exists := c.balances[key]
		if !exists {
			continue
		}

		ch <- prometheus.MustNewConstMetric(
			c.balanceGauge,
			prometheus.GaugeValue,
			balance,
			string(account.Provider),
			account.AccountID,
		)
	}
}
