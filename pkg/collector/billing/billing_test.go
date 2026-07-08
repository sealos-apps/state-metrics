//nolint:testpackage
package billing

import (
	"context"
	"io"
	"maps"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labring/sealos-state-metrics/pkg/collector/base"
	"github.com/labring/sealos-state-metrics/pkg/config"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	log "github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestConfigLoadsDocumentedMongoEnvironmentVariables(t *testing.T) {
	cfg := NewDefaultConfig()
	loader := config.NewEnvConfigLoader(config.WithEnvironment(map[string]string{
		"COLLECTORS_BILLING_MONGO_URI":                   "mongodb://mongo:27017",
		"COLLECTORS_BILLING_MONGO_DATABASE":              "billing-db",
		"COLLECTORS_BILLING_MONGO_BILLING_COLLECTION":    "billing-records",
		"COLLECTORS_BILLING_MONGO_PROPERTIES_COLLECTION": "billing-properties",
		"COLLECTORS_BILLING_SCRAPE_INTERVAL":             "2m",
		"COLLECTORS_BILLING_QUERY_TIMEOUT":               "15s",
	}))

	if err := loader.LoadModuleConfig("collectors.billing", cfg); err != nil {
		t.Fatalf("LoadModuleConfig() error = %v", err)
	}

	if cfg.Mongo.URI != "mongodb://mongo:27017" {
		t.Fatalf("mongo uri = %q", cfg.Mongo.URI)
	}

	if cfg.Mongo.Database != "billing-db" {
		t.Fatalf("mongo database = %q", cfg.Mongo.Database)
	}

	if cfg.Mongo.BillingCollection != "billing-records" {
		t.Fatalf("mongo billing collection = %q", cfg.Mongo.BillingCollection)
	}

	if cfg.Mongo.PropertiesCollection != "billing-properties" {
		t.Fatalf("mongo properties collection = %q", cfg.Mongo.PropertiesCollection)
	}

	if cfg.ScrapeInterval != 2*time.Minute {
		t.Fatalf("scrape interval = %s", cfg.ScrapeInterval)
	}

	if cfg.QueryTimeout != 15*time.Second {
		t.Fatalf("query timeout = %s", cfg.QueryTimeout)
	}
}

func TestAggregateBillingDocuments(t *testing.T) {
	docs := []bson.M{
		{
			"owner":     "alice",
			"namespace": "ns-alice",
			"app_type":  appTypeAPP,
			"status":    billingStatusSettled,
			"app_costs": []any{
				bson.M{
					"used": bson.M{
						resourceCPU:    int64(1000),
						resourceMemory: int64(2048),
					},
				},
			},
		},
		{
			"owner":     "alice",
			"namespace": "ns-alice",
			"app_type":  appTypeAPP,
			"status":    billingStatusSettled,
			"app_costs": []any{
				bson.M{
					"used": bson.M{resourceCPU: int64(300)},
				},
			},
		},
		{
			"owner":     "bob",
			"namespace": "ns-bob",
			"app_type":  appTypeObjectStorage,
			"status":    billingStatusSubscription,
			"app_costs": []any{
				bson.M{
					"used": bson.M{resourceNetwork: int64(4096)},
				},
			},
		},
	}

	windowStart := time.Date(2026, time.July, 8, 0, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(time.Hour)
	snapshot := aggregateBillingDocuments(docs, defaultProperties, windowStart, windowEnd)

	cpuKey := resourceKey{
		Resource: defaultProperties[resourceCPU].Name,
		Unit:     defaultProperties[resourceCPU].Unit,
	}
	if got := snapshot.Resources[cpuKey].Used; got != 1300 {
		t.Fatalf("cpu used = %v, want 1300", got)
	}

	ownerCPUKey := resourceKey{
		Resource:  cpuKey.Resource,
		Unit:      cpuKey.Unit,
		Owner:     "alice",
		Namespace: "ns-alice",
	}
	if got := snapshot.OwnerResources[ownerCPUKey].Used; got != 1300 {
		t.Fatalf("owner cpu used = %v, want 1300", got)
	}

	if got := snapshot.WindowStart; !got.Equal(windowStart) {
		t.Fatalf("window start = %v, want %v", got, windowStart)
	}

	if got := snapshot.WindowEnd; !got.Equal(windowEnd) {
		t.Fatalf("window end = %v, want %v", got, windowEnd)
	}

	networkKey := resourceKey{
		Resource: defaultProperties[resourceNetwork].Name,
		Unit:     defaultProperties[resourceNetwork].Unit,
	}
	if got := snapshot.Resources[networkKey].Used; got != 4096 {
		t.Fatalf("network used = %v, want 4096", got)
	}
}

func TestPollReadsMongoBillingWindowAndCollectsMetrics(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	mongoURI := startMongoContainer(t, ctx)

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	defer func() {
		if err := client.Disconnect(context.Background()); err != nil {
			t.Fatalf("disconnect mongo: %v", err)
		}
	}()

	if err := client.Ping(ctx, nil); err != nil {
		t.Fatalf("ping mongo: %v", err)
	}

	cfg := NewDefaultConfig()
	cfg.Mongo.URI = mongoURI
	cfg.Mongo.Database = "billing-test"
	cfg.QueryTimeout = 10 * time.Second

	windowEnd := time.Now().UTC().Truncate(time.Hour)
	windowStart := windowEnd.Add(-1 * time.Hour)

	seedBillingMongo(t, ctx, client, cfg, windowStart, windowEnd)

	logger := log.New()
	logger.SetOutput(io.Discard)

	c := &Collector{
		BaseCollector: base.NewBaseCollector("billing", log.NewEntry(logger)),
		config:        cfg,
		logger:        log.NewEntry(logger),
		mongoClient:   client,
	}
	c.initMetrics("test")

	if err := c.Poll(ctx); err != nil {
		t.Fatalf("poll billing: %v", err)
	}

	c.mu.RLock()
	snapshot := c.snapshot
	c.mu.RUnlock()

	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}

	if got := snapshot.WindowStart; !got.Equal(windowStart) {
		t.Fatalf("window start = %v, want %v", got, windowStart)
	}

	if got := snapshot.WindowEnd; !got.Equal(windowEnd) {
		t.Fatalf("window end = %v, want %v", got, windowEnd)
	}

	metrics := collectBillingMetrics(t, c)
	windowLabels := map[string]string{
		"window_start": strconv.FormatInt(windowStart.Unix(), 10),
		"window_end":   strconv.FormatInt(windowEnd.Unix(), 10),
	}

	assertGaugeValue(
		t,
		metrics,
		"test_billing_resource_usage",
		mergeLabels(windowLabels, map[string]string{
			"resource": "cpu",
			"unit":     "1m",
		}),
		1500,
	)
	assertGaugeValue(
		t,
		metrics,
		"test_billing_resource_usage",
		mergeLabels(windowLabels, map[string]string{
			"resource": "memory",
			"unit":     "1Mi",
		}),
		2048,
	)
	assertGaugeValue(
		t,
		metrics,
		"test_billing_resource_usage",
		mergeLabels(windowLabels, map[string]string{
			"resource": "network",
			"unit":     "1Mi",
		}),
		4096,
	)
	assertGaugeValue(
		t,
		metrics,
		"test_billing_resource_usage",
		mergeLabels(windowLabels, map[string]string{
			"resource": "custom.gpu",
			"unit":     "1",
		}),
		2,
	)

	assertGaugeValue(
		t,
		metrics,
		"test_billing_owner_resource_usage",
		mergeLabels(windowLabels, map[string]string{
			"owner":     "alice",
			"namespace": "ns-alice",
			"resource":  "cpu",
			"unit":      "1m",
		}),
		1000,
	)
	assertGaugeValue(
		t,
		metrics,
		"test_billing_owner_resource_usage",
		mergeLabels(windowLabels, map[string]string{
			"owner":     "alice",
			"namespace": "ns-alice",
			"resource":  "custom.gpu",
			"unit":      "1",
		}),
		2,
	)
	assertGaugeValue(
		t,
		metrics,
		"test_billing_owner_resource_usage",
		mergeLabels(windowLabels, map[string]string{
			"owner":     "bob",
			"namespace": "ns-bob",
			"resource":  "network",
			"unit":      "1Mi",
		}),
		4096,
	)
	assertGaugeValue(
		t,
		metrics,
		"test_billing_last_success_timestamp_seconds",
		nil,
		float64(snapshot.FinishedAt.Unix()),
	)

	assertMetricAbsent(t, metrics, "test_billing_record_count")
	assertMetricAbsent(t, metrics, "test_billing_window_start_timestamp_seconds")
	assertMetricAbsent(t, metrics, "test_billing_window_end_timestamp_seconds")
	assertLabelAbsent(t, metrics, "app_type")
	assertLabelAbsent(t, metrics, "status")
}

func startMongoContainer(t *testing.T, ctx context.Context) string {
	t.Helper()

	waitStrategy := wait.ForListeningPort("27017/tcp").
		WithStartupTimeout(90 * time.Second)

	ctr, err := testcontainers.Run(
		ctx,
		"mongo:7.0",
		testcontainers.WithExposedPorts("27017/tcp"),
		testcontainers.WithWaitStrategy(waitStrategy),
	)
	if err != nil {
		t.Fatalf("start mongo container: %v", err)
	}

	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			t.Fatalf("terminate mongo container: %v", err)
		}
	})

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("get mongo host: %v", err)
	}

	port, err := ctr.MappedPort(ctx, "27017/tcp")
	if err != nil {
		t.Fatalf("get mongo port: %v", err)
	}

	return "mongodb://" + net.JoinHostPort(host, port.Port())
}

func seedBillingMongo(
	t *testing.T,
	ctx context.Context,
	client *mongo.Client,
	cfg *Config,
	windowStart, windowEnd time.Time,
) {
	t.Helper()

	db := client.Database(cfg.Mongo.Database)

	_, err := db.Collection(cfg.Mongo.PropertiesCollection).InsertMany(ctx, []any{
		bson.M{"enum": 0, "name": "cpu", "unit": "1m"},
		bson.M{"enum": 1, "name": "memory", "unit": "1Mi"},
		bson.M{"enum": 3, "name": "network", "unit": "1Mi"},
		bson.M{"enum": 7, "name": "custom.gpu", "unit": "1"},
	})
	if err != nil {
		t.Fatalf("seed properties: %v", err)
	}

	_, err = db.Collection(cfg.Mongo.BillingCollection).InsertMany(ctx, []any{
		bson.M{
			"time":      windowEnd,
			"type":      billingTypeConsumption,
			"owner":     "alice",
			"namespace": "ns-alice",
			"app_type":  appTypeAPP,
			"status":    billingStatusSettled,
			"app_costs": []any{
				bson.M{
					"name": "launchpad-api",
					"used": bson.M{
						resourceCPU:    int64(1000),
						resourceMemory: int64(2048),
						"7":            int64(2),
					},
				},
			},
		},
		bson.M{
			"time":      windowEnd,
			"type":      billingTypeConsumption,
			"owner":     "bob",
			"namespace": "ns-bob",
			"app_type":  appTypeObjectStorage,
			"status":    billingStatusSubscription,
			"app_costs": []any{
				bson.M{
					"name": "object-storage",
					"used": bson.M{
						resourceCPU:     int64(500),
						resourceNetwork: int64(4096),
					},
				},
			},
		},
		bson.M{
			"time":      windowStart,
			"type":      billingTypeConsumption,
			"owner":     "outside-start",
			"namespace": "ns-outside-start",
			"app_costs": []any{
				bson.M{"used": bson.M{resourceCPU: int64(999)}},
			},
		},
		bson.M{
			"time":      windowEnd,
			"type":      1,
			"owner":     "wrong-type",
			"namespace": "ns-wrong-type",
			"app_costs": []any{
				bson.M{"used": bson.M{resourceCPU: int64(999)}},
			},
		},
	})
	if err != nil {
		t.Fatalf("seed billing: %v", err)
	}
}

type collectedMetric struct {
	name   string
	labels map[string]string
	value  float64
}

func collectBillingMetrics(t *testing.T, c *Collector) []collectedMetric {
	t.Helper()

	ch := make(chan prometheus.Metric, 32)
	c.collect(ch)
	close(ch)

	var metrics []collectedMetric
	for metric := range ch {
		var dtoMetric dto.Metric
		if err := metric.Write(&dtoMetric); err != nil {
			t.Fatalf("write metric: %v", err)
		}

		labels := make(map[string]string, len(dtoMetric.GetLabel()))
		for _, label := range dtoMetric.GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}

		metrics = append(metrics, collectedMetric{
			name:   metricName(metric),
			labels: labels,
			value:  dtoMetric.GetGauge().GetValue(),
		})
	}

	return metrics
}

func metricName(metric prometheus.Metric) string {
	const marker = `fqName: "`

	desc := metric.Desc().String()

	start := strings.Index(desc, marker)
	if start == -1 {
		return ""
	}

	start += len(marker)

	end := strings.Index(desc[start:], `"`)
	if end == -1 {
		return ""
	}

	return desc[start : start+end]
}

func assertGaugeValue(
	t *testing.T,
	metrics []collectedMetric,
	name string,
	labels map[string]string,
	want float64,
) {
	t.Helper()

	for _, metric := range metrics {
		if metric.name != name || !labelsMatch(metric.labels, labels) {
			continue
		}

		if metric.value != want {
			t.Fatalf("%s%v = %v, want %v", name, labels, metric.value, want)
		}

		return
	}

	t.Fatalf("metric %s%v not found", name, labels)
}

func assertMetricAbsent(t *testing.T, metrics []collectedMetric, name string) {
	t.Helper()

	for _, metric := range metrics {
		if metric.name == name {
			t.Fatalf("unexpected metric %s", name)
		}
	}
}

func assertLabelAbsent(t *testing.T, metrics []collectedMetric, labelName string) {
	t.Helper()

	for _, metric := range metrics {
		if _, ok := metric.labels[labelName]; ok {
			t.Fatalf("unexpected label %q on metric %s", labelName, metric.name)
		}
	}
}

func labelsMatch(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}

	for key, value := range want {
		if got[key] != value {
			return false
		}
	}

	return true
}

func mergeLabels(baseLabels, extraLabels map[string]string) map[string]string {
	merged := make(map[string]string, len(baseLabels)+len(extraLabels))
	maps.Copy(merged, baseLabels)
	maps.Copy(merged, extraLabels)

	return merged
}
