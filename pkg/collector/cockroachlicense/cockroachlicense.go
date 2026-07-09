package cockroachlicense

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labring/sealos-state-metrics/pkg/collector/base"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
)

// ClusterStatus stores the most recent license scrape result for one cluster.
type ClusterStatus struct {
	Name             string
	Up               bool
	HasLicense       bool
	LicenseType      string
	LicenseTypeValue float64
	Environment      string
	LicenseID        string
	OrganizationID   string
	OrganizationName string
	ExpiresAt        int64
	SecondsUntil     float64
	LastScrape       int64
	Error            string
}

// Collector implements CockroachDB license monitoring.
type Collector struct {
	*base.BaseCollector

	config *Config
	logger *log.Entry

	mu       sync.RWMutex
	clusters map[string]*ClusterStatus

	upGauge                 *prometheus.Desc
	infoGauge               *prometheus.Desc
	typeGauge               *prometheus.Desc
	expiryGauge             *prometheus.Desc
	secondsUntilExpiryGauge *prometheus.Desc
	lastScrapeGauge         *prometheus.Desc
}

func (c *Collector) initMetrics(namespace string) {
	c.upGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "cockroach_license", "up"),
		"Whether CockroachDB license is installed and not expired (1=valid, 0=missing, expired, or collection failure)",
		[]string{"cluster"},
		nil,
	)
	c.infoGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "cockroach_license", "info"),
		"CockroachDB license information. Value is 1 when a license is installed, 0 otherwise.",
		[]string{
			"cluster",
			"type",
			"environment",
			"license_id",
			"organization_id",
			"organization_name",
		},
		nil,
	)
	c.typeGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "cockroach_license", "type"),
		"CockroachDB license type as numeric value (0=Unknown/None, 1=Enterprise, 2=Evaluation, 3=Free, 4=Trial, 5=NonCommercial)",
		[]string{"cluster", "type"},
		nil,
	)
	c.expiryGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "cockroach_license", "expiry_timestamp_seconds"),
		"CockroachDB license expiration timestamp in Unix seconds. Value is 0 when no license is installed.",
		[]string{"cluster"},
		nil,
	)
	c.secondsUntilExpiryGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "cockroach_license", "seconds_until_expiry"),
		"Seconds until CockroachDB license expiration. Value is 0 when no license is installed.",
		[]string{"cluster"},
		nil,
	)
	c.lastScrapeGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "cockroach_license", "last_scrape_timestamp_seconds"),
		"Unix timestamp of the last CockroachDB license collection attempt.",
		[]string{"cluster"},
		nil,
	)

	c.MustRegisterDesc(c.upGauge)
	c.MustRegisterDesc(c.infoGauge)
	c.MustRegisterDesc(c.typeGauge)
	c.MustRegisterDesc(c.expiryGauge)
	c.MustRegisterDesc(c.secondsUntilExpiryGauge)
	c.MustRegisterDesc(c.lastScrapeGauge)
}

func (c *Collector) HasSynced() bool {
	return true
}

func (c *Collector) Interval() time.Duration {
	return c.config.CheckInterval
}

func (c *Collector) pollLoop(ctx context.Context) {
	c.RunPollLoop(ctx, c.Poll, base.PollLoopOptions{
		Interval:  c.config.CheckInterval,
		Operation: "cockroachlicense",
	})
}

func (c *Collector) Poll(ctx context.Context) error {
	statuses := make(map[string]*ClusterStatus, len(c.config.Clusters))

	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for _, cluster := range c.config.Clusters {
		wg.Add(1)
		go func(cluster ClusterConfig) {
			defer wg.Done()

			status := c.collectCluster(ctx, cluster)

			mu.Lock()
			statuses[cluster.Name] = status
			mu.Unlock()
		}(cluster)
	}

	wg.Wait()

	c.mu.Lock()
	c.clusters = statuses
	c.mu.Unlock()

	return nil
}

func (c *Collector) collectCluster(ctx context.Context, cluster ClusterConfig) *ClusterStatus {
	status := &ClusterStatus{
		Name:       cluster.Name,
		LastScrape: time.Now().Unix(),
	}

	checkCtx, cancel := context.WithTimeout(ctx, c.config.CheckTimeout)
	defer cancel()

	licenseString, err := c.queryLicense(checkCtx, cluster.DSN)
	if err != nil {
		status.Error = err.Error()
		c.logger.WithField("cluster", cluster.Name).WithError(err).
			Warn("Failed to query Cockroach license")
		return status
	}

	if licenseString == "" {
		c.logger.Warnf(
			"No license string returned for cluster %q, treating as no license installed",
			cluster.Name,
		)
	}

	info, err := decodeLicense(licenseString)
	if err != nil {
		status.Error = err.Error()
		c.logger.WithField("cluster", cluster.Name).WithError(err).
			Warn("Failed to decode Cockroach license")
		return status
	}

	if info == nil {
		return status
	}

	status.HasLicense = true
	status.LicenseType = licenseTypeString(info.licenseType)
	status.LicenseTypeValue = licenseTypeValue(info.licenseType)
	status.Environment = environmentString(info.environment)
	status.LicenseID = info.licenseID
	status.OrganizationID = info.organizationID
	status.OrganizationName = info.organizationName
	status.ExpiresAt = info.validUntilUnixSec
	status.SecondsUntil = time.Until(time.Unix(info.validUntilUnixSec, 0)).Seconds()
	status.Up = status.SecondsUntil > 0

	return status
}

func (c *Collector) queryLicense(ctx context.Context, dsn string) (string, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return "", err
	}
	defer conn.Close(context.Background())

	var license string
	if err := conn.QueryRow(ctx, "SHOW CLUSTER SETTING enterprise.license").
		Scan(&license); err != nil {
		return "", err
	}

	return license, nil
}

func (c *Collector) collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, status := range c.clusters {
		ch <- prometheus.MustNewConstMetric(
			c.upGauge,
			prometheus.GaugeValue,
			boolToFloat64(status.Up),
			status.Name,
		)

		ch <- prometheus.MustNewConstMetric(
			c.infoGauge,
			prometheus.GaugeValue,
			boolToFloat64(status.HasLicense),
			status.Name,
			status.LicenseType,
			status.Environment,
			status.LicenseID,
			status.OrganizationID,
			status.OrganizationName,
		)

		ch <- prometheus.MustNewConstMetric(
			c.typeGauge,
			prometheus.GaugeValue,
			status.LicenseTypeValue,
			status.Name,
			status.LicenseType,
		)

		ch <- prometheus.MustNewConstMetric(
			c.expiryGauge,
			prometheus.GaugeValue,
			float64(status.ExpiresAt),
			status.Name,
		)

		ch <- prometheus.MustNewConstMetric(
			c.secondsUntilExpiryGauge,
			prometheus.GaugeValue,
			status.SecondsUntil,
			status.Name,
		)

		ch <- prometheus.MustNewConstMetric(
			c.lastScrapeGauge,
			prometheus.GaugeValue,
			float64(status.LastScrape),
			status.Name,
		)
	}
}

func (c *Collector) health() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.clusters) == 0 {
		return errors.New("no Cockroach license status has been collected")
	}

	for _, status := range c.clusters {
		if status.Up {
			return nil
		}
	}

	return errors.New("all Cockroach license collection targets are down")
}

func boolToFloat64(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
