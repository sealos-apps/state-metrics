package domain

import (
	"context"
	"sync"
	"time"

	"github.com/labring/sealos-state-metrics/pkg/collector/base"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
)

// Collector collects domain metrics
type Collector struct {
	*base.BaseCollector

	runtime *runtimeConfig
	checker *DomainChecker
	logger  *log.Entry

	mu      sync.RWMutex
	ips     map[string]*IPHealth     // key: domain/ip
	domains map[string]*DomainHealth // key: domain

	// Metrics
	domainHealth       *prometheus.Desc
	domainDNSStatus    *prometheus.Desc
	domainStatus       *prometheus.Desc
	domainCertExpiry   *prometheus.Desc
	domainResponseTime *prometheus.Desc
}

// initMetrics initializes Prometheus metric descriptors
func (c *Collector) initMetrics(namespace string) {
	c.domainHealth = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "domain", "health"),
		"Domain-level health metrics",
		[]string{"domain", "type"},
		nil,
	)
	c.domainDNSStatus = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "domain", "dns_status"),
		"Domain DNS status (1=ok, 0=error)",
		[]string{"domain", "error_type"},
		nil,
	)
	c.domainStatus = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "domain", "status"),
		"Domain IP status (1=ok, 0=error)",
		[]string{"domain", "ip", "check_type", "error_type"},
		nil,
	)
	c.domainCertExpiry = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "domain", "cert_expiry_seconds"),
		"Domain certificate expiry in seconds",
		[]string{"domain", "ip", "error_type"},
		nil,
	)
	c.domainResponseTime = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "domain", "response_time_seconds"),
		"Domain IP response time in seconds",
		[]string{"domain", "ip"},
		nil,
	)

	// Register descriptors
	c.MustRegisterDesc(c.domainHealth)
	c.MustRegisterDesc(c.domainDNSStatus)
	c.MustRegisterDesc(c.domainStatus)
	c.MustRegisterDesc(c.domainCertExpiry)
	c.MustRegisterDesc(c.domainResponseTime)
}

// HasSynced returns true (polling collector is always synced)
func (c *Collector) HasSynced() bool {
	return true
}

// Interval returns the polling interval
func (c *Collector) Interval() time.Duration {
	return c.runtime.checkInterval
}

// Poll performs one check cycle
func (c *Collector) Poll(ctx context.Context) error {
	if len(c.runtime.domains) == 0 {
		c.logger.Debug("No domains configured for monitoring")
		return nil
	}

	c.logger.WithField("count", len(c.runtime.domains)).Info("Starting domain health checks")

	// Create new maps to store results
	newIPs := make(map[string]*IPHealth)
	newDomains := make(map[string]*DomainHealth)

	var mu sync.Mutex

	// Check domains concurrently
	var wg sync.WaitGroup
	for _, domain := range c.runtime.domains {
		wg.Go(func() {
			domainHealth, ipHealths := c.checker.CheckIPs(ctx, domain, c.logger)

			// Add results to new maps
			mu.Lock()

			// Store domain-level health
			newDomains[domain.endpoint] = domainHealth

			// Store IP-level health
			for _, ipHealth := range ipHealths {
				key := ipKey(ipHealth.Domain, ipHealth.IP)
				newIPs[key] = ipHealth
			}

			mu.Unlock()
		})
	}

	wg.Wait()

	// Atomically replace the old maps with the new ones
	c.mu.Lock()
	c.ips = newIPs
	c.domains = newDomains
	c.mu.Unlock()

	c.logger.WithField("count", len(c.runtime.domains)).Info("Domain health checks completed")

	return nil
}

// pollLoop runs the polling loop
func (c *Collector) pollLoop(ctx context.Context) {
	c.RunPollLoop(ctx, c.Poll, base.PollLoopOptions{
		Interval:  c.runtime.checkInterval,
		Operation: "domain",
	})
}

// collect collects metrics
func (c *Collector) collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Emit domain-level health metrics
	for _, domainHealth := range c.domains {
		// Resolve status (1=success, 0=failure)
		ch <- prometheus.MustNewConstMetric(
			c.domainHealth,
			prometheus.GaugeValue,
			boolToFloat64(domainHealth.ResolveOk),
			domainHealth.Domain,
			"resolve",
		)

		// IP count
		ch <- prometheus.MustNewConstMetric(
			c.domainHealth,
			prometheus.GaugeValue,
			float64(domainHealth.IPCount),
			domainHealth.Domain,
			"ip_count",
		)

		// Healthy IPs count
		ch <- prometheus.MustNewConstMetric(
			c.domainHealth,
			prometheus.GaugeValue,
			float64(domainHealth.HealthyIPs),
			domainHealth.Domain,
			"healthy_ips",
		)

		// Unhealthy IPs count
		ch <- prometheus.MustNewConstMetric(
			c.domainHealth,
			prometheus.GaugeValue,
			float64(domainHealth.UnhealthyIPs),
			domainHealth.Domain,
			"unhealthy_ips",
		)
	}

	// Emit domain-level DNS status once per domain.
	emittedDNS := make(map[string]struct{}, len(c.ips))
	for _, ipHealth := range c.ips {
		if ipHealth.DNSChecked {
			if _, exists := emittedDNS[ipHealth.Domain]; !exists {
				emittedDNS[ipHealth.Domain] = struct{}{}

				ch <- prometheus.MustNewConstMetric(
					c.domainDNSStatus,
					prometheus.GaugeValue,
					boolToFloat64(ipHealth.DNSOk),
					ipHealth.Domain,
					string(ipHealth.DNSErrorType),
				)
			}
		}
	}

	// Emit IP-level metrics
	for _, ipHealth := range c.ips {
		// HTTP status
		if c.runtime.includeHTTPCheck && ipHealth.HTTPChecked {
			ch <- prometheus.MustNewConstMetric(
				c.domainStatus,
				prometheus.GaugeValue,
				boolToFloat64(ipHealth.HTTPOk),
				ipHealth.Domain,
				ipHealth.IP,
				"http",
				string(ipHealth.HTTPErrorType),
			)

			if ipHealth.HTTPOk {
				ch <- prometheus.MustNewConstMetric(
					c.domainResponseTime,
					prometheus.GaugeValue,
					ipHealth.ResponseTime.Seconds(),
					ipHealth.Domain,
					ipHealth.IP,
				)
			}
		}

		// Certificate status
		if c.runtime.includeCertCheck && ipHealth.CertChecked {
			ch <- prometheus.MustNewConstMetric(
				c.domainStatus,
				prometheus.GaugeValue,
				boolToFloat64(ipHealth.CertOk),
				ipHealth.Domain,
				ipHealth.IP,
				"cert",
				string(ipHealth.CertErrorType),
			)

			if ipHealth.CertOk && ipHealth.CertExpiry > 0 {
				ch <- prometheus.MustNewConstMetric(
					c.domainCertExpiry,
					prometheus.GaugeValue,
					ipHealth.CertExpiry.Seconds(),
					ipHealth.Domain,
					ipHealth.IP,
					string(ipHealth.CertErrorType),
				)
			}
		}
	}
}

// ipKey generates a unique key for an IP
func ipKey(domain, ip string) string {
	return domain + "/" + ip
}

// boolToFloat64 converts a boolean to a float64
func boolToFloat64(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}
