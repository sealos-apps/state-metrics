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
	// localPgClient selects discovered users belonging to this region.
	localPgClient *pgxpool.Pool

	// Prometheus metrics
	balanceGauge *prometheus.Desc

	// Internal state
	mu       sync.RWMutex
	balances map[string]balanceSample
}

type balanceSample struct {
	User      UserConfig
	Balance   float64
	EmitQuota bool
	Source    string
}

// initMetrics initializes Prometheus metric descriptors
func (c *Collector) initMetrics(namespace string) {
	c.balanceGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "userbalance", "balance"),
		"Current balance for each sealos user",
		[]string{"region", "uuid", "uid", "owner", "type", "level", "source"},
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

// Poll queries configured user accounts and optionally discovers positive-balance users.
func (c *Collector) Poll(ctx context.Context) error {
	if len(c.config.UserConfig) == 0 && !c.config.PositiveBalanceUsers {
		c.logger.Debug("No sealos user configured for monitoring")
		return nil
	}

	newBalances := make(map[string]balanceSample, len(c.config.UserConfig))
	configuredUsers := make(map[string]struct{}, len(c.config.UserConfig))

	if len(c.config.UserConfig) > 0 {
		c.logger.WithField("count", len(c.config.UserConfig)).Info("Starting user balance checks")

		for _, user := range c.config.UserConfig {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			balance, err := c.QueryBalance(ctx, user)
			if err != nil {
				c.logger.WithFields(log.Fields{
					"user_id": user.UID,
				}).WithError(err).Error("Failed to query sealos user balance")

				continue
			}

			user = normalizeUserLabels(user)

			newBalances[userKey(user)] = balanceSample{
				User:      user,
				Balance:   balance,
				EmitQuota: true,
				Source:    "configured",
			}
			for _, key := range userIdentityKeys(user) {
				configuredUsers[key] = struct{}{}
			}

			c.logger.WithFields(log.Fields{
				"region":  user.Region,
				"uid":     user.UID,
				"balance": balance,
			}).Debug("User balance updated")
		}
	}

	if c.config.PositiveBalanceUsers {
		samples, err := c.QueryPositiveBalances(ctx)
		if err != nil {
			if len(c.config.UserConfig) == 0 {
				return err
			}

			c.logger.WithError(err).
				Warn("Positive-balance user discovery failed, using configured users only")
		} else {
			if len(samples) > 0 && c.config.DatabaseConfig.LocalDSN != "" {
				if c.localPgClient == nil {
					c.logger.Warn("Local user database unavailable, skipping discovered users")

					samples = nil
				} else {
					owners, ownerErr := c.QueryOwners(ctx, sampleUUIDs(samples))
					if ownerErr != nil {
						c.logger.WithError(ownerErr).
							Warn("User owner query failed, skipping discovered users")

						samples = nil
					} else {
						regionalSamples := make([]balanceSample, 0, len(samples))
						for _, sample := range samples {
							for _, owner := range owners[sample.User.UUID] {
								if owner == "" {
									continue
								}

								sample.User.Owner = owner
								regionalSamples = append(regionalSamples, sample)
							}
						}

						samples = regionalSamples
					}
				}
			}

			discoveredCount := 0
			for _, sample := range samples {
				if userConfigured(sample.User, configuredUsers) {
					continue
				}

				sample.User = normalizeUserLabels(sample.User)
				sample.Source = "discovered"
				newBalances[userKey(sample.User)] = sample
				discoveredCount++
			}

			c.logger.WithField("count", discoveredCount).
				Info("Positive-balance users updated")
		}
	}

	c.mu.Lock()
	c.balances = newBalances
	c.mu.Unlock()

	return nil
}

func sampleUUIDs(samples []balanceSample) []string {
	uuids := make([]string, 0, len(samples))
	for _, sample := range samples {
		if sample.User.UUID != "" {
			uuids = append(uuids, sample.User.UUID)
		}
	}

	return uuids
}

// collect implements the collect method for Prometheus
func (c *Collector) collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, sample := range c.balances {
		user := sample.User

		ch <- prometheus.MustNewConstMetric(
			c.balanceGauge,
			prometheus.GaugeValue,
			sample.Balance,
			user.Region,
			user.UUID,
			user.UID,
			user.Owner,
			user.Type,
			user.Level,
			sample.Source,
		)

		if !sample.EmitQuota {
			continue
		}

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
			sample.Source,
		)
	}
}

func userKey(user UserConfig) string {
	return user.Region + ":" + user.UUID + ":" + user.UID + ":" + user.Owner + ":" + user.Type + ":" + user.Level
}

func normalizeUserLabels(user UserConfig) UserConfig {
	if user.Region == "" {
		user.Region = "default"
	}

	if user.Level == "" {
		user.Level = "default"
	}

	return user
}

func userConfigured(user UserConfig, configuredUsers map[string]struct{}) bool {
	for _, key := range userIdentityKeys(user) {
		if _, ok := configuredUsers[key]; ok {
			return true
		}
	}

	return false
}

func userIdentityKeys(user UserConfig) []string {
	keys := make([]string, 0, 3)
	if user.UID != "" {
		keys = append(keys, "uid:"+user.UID)
	}

	if user.UUID != "" {
		keys = append(keys, "uuid:"+user.UUID)
	}

	if user.Owner != "" {
		keys = append(keys, "owner:"+user.Owner)
	}

	return keys
}
