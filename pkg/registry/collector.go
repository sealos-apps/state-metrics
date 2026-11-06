package registry

import (
	"sync"
	"time"

	"github.com/labring/sealos-state-metrics/pkg/collector"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	log "github.com/sirupsen/logrus"
)

// collectorResult holds the result of a collector execution
type collectorResult struct {
	name     string
	duration time.Duration
	success  bool
}

// PrometheusCollector wraps the registry as a prometheus.Collector.
// This allows all collectors to be registered with a single Prometheus registry.
type PrometheusCollector struct {
	registry *Registry

	// Duration metrics
	collectorDuration *prometheus.Desc
	collectorSuccess  *prometheus.Desc
}

// NewPrometheusCollector creates a new PrometheusCollector
func NewPrometheusCollector(registry *Registry, namespace string) *PrometheusCollector {
	return &PrometheusCollector{
		registry: registry,
		collectorDuration: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "state_metric", "collector_duration_seconds"),
			"Duration of collector scrape in seconds",
			[]string{"collector", "instance"},
			nil,
		),
		collectorSuccess: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "state_metric", "collector_success"),
			"Whether collector scrape was successful (1=success, 0=failure)",
			[]string{"collector", "instance"},
			nil,
		),
	}
}

// getInstance returns the current instance identity from the registry
// This is called on each collect to ensure the instance is always up-to-date after reloads
func (pc *PrometheusCollector) getInstance() string {
	pc.registry.mu.RLock()
	defer pc.registry.mu.RUnlock()
	return pc.registry.instance
}

// Describe implements prometheus.Collector
func (pc *PrometheusCollector) Describe(ch chan<- *prometheus.Desc) {
	pc.registry.mu.RLock()
	collectors := pc.registry.collectors
	pc.registry.mu.RUnlock()

	// Describe our own metrics
	ch <- pc.collectorDuration

	ch <- pc.collectorSuccess

	// Describe all collectors concurrently
	var wg sync.WaitGroup
	for _, c := range collectors {
		wg.Go(func() {
			c.Describe(ch)
		})
	}

	wg.Wait()
}

// collectFromCollector executes a single collector and returns the result
func collectFromCollector(
	name string,
	col collector.Collector,
	ch chan<- prometheus.Metric,
	logger *log.Entry,
) collectorResult {
	start := time.Now()
	success := true

	// Collect metrics with panic recovery
	func() {
		defer func() {
			if r := recover(); r != nil {
				logger.WithFields(log.Fields{
					"collector": name,
					"panic":     r,
				}).Error("Collector panicked during collection")

				success = false
			}
		}()

		col.Collect(ch)
	}()

	duration := time.Since(start)

	// Log slow collectors
	if duration > slowCollectorThreshold {
		logger.WithFields(log.Fields{
			"collector": name,
			"duration":  duration,
		}).Warn("Slow collector detected")
	}

	return collectorResult{
		name:     name,
		duration: duration,
		success:  success,
	}
}

// emitCollectorMetrics emits duration and success metrics for collectors
func (pc *PrometheusCollector) emitCollectorMetrics(
	results []collectorResult,
	ch chan<- prometheus.Metric,
) {
	instance := pc.getInstance()
	for _, result := range results {
		ch <- prometheus.MustNewConstMetric(
			pc.collectorDuration,
			prometheus.GaugeValue,
			result.duration.Seconds(),
			result.name,
			instance,
		)

		successValue := 0.0
		if result.success {
			successValue = 1.0
		}

		ch <- prometheus.MustNewConstMetric(
			pc.collectorSuccess,
			prometheus.GaugeValue,
			successValue,
			result.name,
			instance,
		)
	}
}

// Collect implements prometheus.Collector
func (pc *PrometheusCollector) Collect(ch chan<- prometheus.Metric) {
	// Copy collectors map and instance to reduce lock contention
	pc.registry.mu.RLock()
	collectors := pc.registry.collectors
	instance := pc.registry.instance
	pc.registry.mu.RUnlock()

	logger := log.WithField("module", "registry")

	// Setup metric wrapper if instance is configured
	metricCh := ch

	var wrapperWg sync.WaitGroup

	if instance != "" {
		wrapperCh := make(chan prometheus.Metric, 100)
		metricCh = wrapperCh

		wrapperWg.Go(func() {
			wrapMetricsWithInstance(wrapperCh, ch, instance)
		})
	}

	// Collect from all collectors concurrently
	var collectWg sync.WaitGroup

	resultCh := make(chan collectorResult, len(collectors))

	for name, c := range collectors {
		collectWg.Go(func() {
			result := collectFromCollector(name, c, metricCh, logger)
			resultCh <- result
		})
	}

	// Wait for all collectors to finish
	collectWg.Wait()
	close(resultCh)

	// If wrapper is running, close wrapper channel and wait for it
	if instance != "" {
		close(metricCh)
		wrapperWg.Wait()
	}

	// Collect results and emit performance metrics
	results := make([]collectorResult, 0, len(collectors))
	for result := range resultCh {
		results = append(results, result)
	}

	pc.emitCollectorMetrics(results, ch)
}

// wrapMetricsWithInstance wraps metrics by adding instance label
func wrapMetricsWithInstance(
	source <-chan prometheus.Metric,
	dest chan<- prometheus.Metric,
	instance string,
) {
	for metric := range source {
		wrappedMetric := &metricWithInstance{
			Metric:   metric,
			instance: instance,
		}
		dest <- wrappedMetric
	}
}

// metricWithInstance wraps a prometheus.Metric and adds instance label
type metricWithInstance struct {
	prometheus.Metric
	instance string
}

// Write implements prometheus.Metric by adding instance label
func (m *metricWithInstance) Write(out *dto.Metric) error {
	// First, write the original metric
	if err := m.Metric.Write(out); err != nil {
		return err
	}

	// Add instance label
	out.Label = append(out.Label, &dto.LabelPair{
		Name:  new("instance"),
		Value: new(m.instance),
	})

	return nil
}
