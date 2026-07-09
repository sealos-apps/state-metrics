package registryproxy

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/labring/sealos-state-metrics/pkg/collector/base"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
)

// Collector collects registry proxy metrics.
type Collector struct {
	*base.BaseCollector

	runtime *runtimeConfig
	checker *RegistryChecker
	logger  *log.Entry

	mu         sync.RWMutex
	ips        map[string]*IPHealth
	registries map[string]*RegistryHealth

	registryHealth       *prometheus.Desc
	registryDNSStatus    *prometheus.Desc
	registryStatus       *prometheus.Desc
	registryResponseTime *prometheus.Desc
}

func (c *Collector) initMetrics(namespace string) {
	c.registryHealth = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "registry_proxy", "health"),
		"Registry proxy aggregate health metrics",
		[]string{"endpoint", "scheme", "host", "port", "info", "repository", "reference", "type"},
		nil,
	)
	c.registryDNSStatus = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "registry_proxy", "dns_status"),
		"Registry proxy DNS status (1=ok, 0=error)",
		[]string{
			"endpoint",
			"scheme",
			"host",
			"port",
			"info",
			"repository",
			"reference",
			"error_type",
		},
		nil,
	)
	c.registryStatus = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "registry_proxy", "status"),
		"Registry proxy API and manifest status (1=ok, 0=error)",
		[]string{
			"endpoint",
			"scheme",
			"host",
			"port",
			"info",
			"repository",
			"reference",
			"ip",
			"check_type",
			"status_code",
			"error_type",
		},
		nil,
	)
	c.registryResponseTime = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "registry_proxy", "response_time_seconds"),
		"Registry proxy API and manifest response time in seconds",
		[]string{
			"endpoint",
			"scheme",
			"host",
			"port",
			"info",
			"repository",
			"reference",
			"ip",
			"check_type",
		},
		nil,
	)

	c.MustRegisterDesc(c.registryHealth)
	c.MustRegisterDesc(c.registryDNSStatus)
	c.MustRegisterDesc(c.registryStatus)
	c.MustRegisterDesc(c.registryResponseTime)
}

// HasSynced returns true because this is a polling collector.
func (c *Collector) HasSynced() bool {
	return true
}

// Interval returns the polling interval.
func (c *Collector) Interval() time.Duration {
	return c.runtime.checkInterval
}

// Poll performs one check cycle.
func (c *Collector) Poll(ctx context.Context) error {
	if len(c.runtime.registries) == 0 {
		c.logger.Debug("No registry proxies configured for monitoring")
		return nil
	}

	c.logger.WithField("count", len(c.runtime.registries)).Info("Starting registry proxy checks")

	newIPs := make(map[string]*IPHealth)
	newRegistries := make(map[string]*RegistryHealth)

	var mu sync.Mutex

	var wg sync.WaitGroup

	for _, registry := range c.runtime.registries {
		wg.Go(func() {
			registryHealth, ipHealths := c.checker.CheckIPs(ctx, registry, c.logger)

			mu.Lock()

			newRegistries[monitoredRegistryKey(registry)] = registryHealth

			for _, ipHealth := range ipHealths {
				newIPs[registryIPKey(ipHealth)] = ipHealth
			}

			mu.Unlock()
		})
	}

	wg.Wait()

	c.mu.Lock()
	c.ips = newIPs
	c.registries = newRegistries
	c.mu.Unlock()

	c.logger.WithField("count", len(c.runtime.registries)).Info("Registry proxy checks completed")

	return nil
}

func (c *Collector) pollLoop(ctx context.Context) {
	c.RunPollLoop(ctx, c.Poll, base.PollLoopOptions{
		Interval:  c.runtime.checkInterval,
		Operation: "registryproxy",
	})
}

func (c *Collector) collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, registryHealth := range c.registries {
		labelValues := registryHealthLabels(registryHealth)

		ch <- prometheus.MustNewConstMetric(
			c.registryHealth,
			prometheus.GaugeValue,
			boolToFloat64(registryHealth.ResolveOk),
			append(labelValues, "resolve")...,
		)

		ch <- prometheus.MustNewConstMetric(
			c.registryHealth,
			prometheus.GaugeValue,
			float64(registryHealth.IPCount),
			append(labelValues, "ip_count")...,
		)

		ch <- prometheus.MustNewConstMetric(
			c.registryHealth,
			prometheus.GaugeValue,
			float64(registryHealth.HealthyIPs),
			append(labelValues, "healthy_ips")...,
		)

		ch <- prometheus.MustNewConstMetric(
			c.registryHealth,
			prometheus.GaugeValue,
			float64(registryHealth.UnhealthyIPs),
			append(labelValues, "unhealthy_ips")...,
		)
	}

	emittedDNS := make(map[string]struct{}, len(c.ips))
	for _, ipHealth := range c.ips {
		if !ipHealth.DNSChecked {
			continue
		}

		dnsKey := ipHealthConfigKey(ipHealth)
		if _, exists := emittedDNS[dnsKey]; exists {
			continue
		}

		emittedDNS[dnsKey] = struct{}{}

		ch <- prometheus.MustNewConstMetric(
			c.registryDNSStatus,
			prometheus.GaugeValue,
			boolToFloat64(ipHealth.DNSOk),
			ipHealth.Endpoint,
			ipHealth.Scheme,
			ipHealth.Host,
			strconv.Itoa(ipHealth.Port),
			ipHealth.Info,
			ipHealth.Repository,
			ipHealth.Reference,
			string(ipHealth.DNSErrorType),
		)
	}

	for _, ipHealth := range c.ips {
		if ipHealth.APIChecked {
			c.emitStatusMetric(
				ch,
				ipHealth,
				"api",
				ipHealth.APIOk,
				ipHealth.APIStatusCode,
				ipHealth.APIErrorType,
			)

			if ipHealth.APIResponseTime > 0 {
				c.emitResponseTimeMetric(ch, ipHealth, "api", ipHealth.APIResponseTime)
			}
		}

		if ipHealth.ManifestChecked {
			c.emitStatusMetric(
				ch,
				ipHealth,
				"manifest",
				ipHealth.ManifestOk,
				ipHealth.ManifestStatus,
				ipHealth.ManifestErrorType,
			)

			if ipHealth.ManifestTime > 0 {
				c.emitResponseTimeMetric(ch, ipHealth, "manifest", ipHealth.ManifestTime)
			}
		}
	}
}

func (c *Collector) emitStatusMetric(
	ch chan<- prometheus.Metric,
	ipHealth *IPHealth,
	checkType string,
	ok bool,
	statusCode int,
	errorType ErrorType,
) {
	ch <- prometheus.MustNewConstMetric(
		c.registryStatus,
		prometheus.GaugeValue,
		boolToFloat64(ok),
		ipHealth.Endpoint,
		ipHealth.Scheme,
		ipHealth.Host,
		strconv.Itoa(ipHealth.Port),
		ipHealth.Info,
		ipHealth.Repository,
		ipHealth.Reference,
		ipHealth.IP,
		checkType,
		strconv.Itoa(statusCode),
		string(errorType),
	)
}

func (c *Collector) emitResponseTimeMetric(
	ch chan<- prometheus.Metric,
	ipHealth *IPHealth,
	checkType string,
	responseTime time.Duration,
) {
	ch <- prometheus.MustNewConstMetric(
		c.registryResponseTime,
		prometheus.GaugeValue,
		responseTime.Seconds(),
		ipHealth.Endpoint,
		ipHealth.Scheme,
		ipHealth.Host,
		strconv.Itoa(ipHealth.Port),
		ipHealth.Info,
		ipHealth.Repository,
		ipHealth.Reference,
		ipHealth.IP,
		checkType,
	)
}

func registryHealthLabels(registryHealth *RegistryHealth) []string {
	return []string{
		registryHealth.Endpoint,
		registryHealth.Scheme,
		registryHealth.Host,
		strconv.Itoa(registryHealth.Port),
		registryHealth.Info,
		registryHealth.Repository,
		registryHealth.Reference,
	}
}

func registryIPKey(ipHealth *IPHealth) string {
	return ipHealthConfigKey(ipHealth) + "/" + ipHealth.IP
}

func monitoredRegistryKey(registry monitoredRegistry) string {
	return registry.endpoint + "/" + registry.info + "/" + registry.repository + "/" + registry.reference
}

func ipHealthConfigKey(ipHealth *IPHealth) string {
	return ipHealth.Endpoint + "/" + ipHealth.Info + "/" + ipHealth.Repository + "/" + ipHealth.Reference
}

func boolToFloat64(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}
