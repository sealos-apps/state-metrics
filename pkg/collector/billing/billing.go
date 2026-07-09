package billing

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"sync"
	"time"

	"github.com/labring/sealos-state-metrics/pkg/collector/base"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Collector reads finalized Sealos billing records from MongoDB.
type Collector struct {
	*base.BaseCollector

	config      *Config
	logger      *log.Entry
	mongoClient *mongo.Client

	mu       sync.RWMutex
	snapshot *BillingSnapshot

	resourceUsageGauge       *prometheus.Desc
	ownerResourceUsageGauge  *prometheus.Desc
	resourceAmountGauge      *prometheus.Desc
	ownerResourceAmountGauge *prometheus.Desc
	lastSuccessGauge         *prometheus.Desc
}

func (c *Collector) initMetrics(namespace string) {
	resourceLabels := []string{"window_start", "window_end", "resource", "unit"}
	resourceAmountLabels := []string{"window_start", "window_end", "resource"}
	ownerResourceLabels := []string{
		"window_start",
		"window_end",
		"owner",
		"namespace",
		"resource",
		"unit",
	}
	ownerResourceAmountLabels := []string{
		"window_start",
		"window_end",
		"owner",
		"namespace",
		"resource",
	}

	c.resourceUsageGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "billing", "resource_usage"),
		"Aggregated billed resource usage from Sealos billing records over the previous complete hourly billing window in the raw Sealos billing unit.",
		resourceLabels,
		nil,
	)
	c.ownerResourceUsageGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "billing_owner", "resource_usage"),
		"Aggregated billed resource usage by Sealos owner and namespace over the previous complete hourly billing window.",
		ownerResourceLabels,
		nil,
	)
	c.resourceAmountGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "billing", "resource_amount"),
		"Aggregated Sealos billing amount by resource over the previous complete hourly billing window in the raw Sealos amount unit.",
		resourceAmountLabels,
		nil,
	)
	c.ownerResourceAmountGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "billing_owner", "resource_amount"),
		"Aggregated Sealos billing amount by Sealos owner, namespace, and resource over the previous complete hourly billing window.",
		ownerResourceAmountLabels,
		nil,
	)
	c.lastSuccessGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "billing", "last_success_timestamp_seconds"),
		"Unix timestamp of the last successful Sealos billing collection.",
		nil,
		nil,
	)

	c.MustRegisterDesc(c.resourceUsageGauge)
	c.MustRegisterDesc(c.ownerResourceUsageGauge)
	c.MustRegisterDesc(c.resourceAmountGauge)
	c.MustRegisterDesc(c.ownerResourceAmountGauge)
	c.MustRegisterDesc(c.lastSuccessGauge)
}

func (c *Collector) HasSynced() bool {
	return true
}

func (c *Collector) Interval() time.Duration {
	return c.config.ScrapeInterval
}

func (c *Collector) pollLoop(ctx context.Context) {
	c.RunPollLoop(ctx, c.Poll, base.PollLoopOptions{
		Interval:  c.config.ScrapeInterval,
		Operation: "billing",
	})
}

func (c *Collector) Poll(ctx context.Context) error {
	startedAt := time.Now()

	if c.mongoClient == nil {
		return errors.New("mongo client is nil")
	}

	queryCtx, cancel := context.WithTimeout(ctx, c.config.QueryTimeout)
	defer cancel()

	properties, err := c.loadProperties(queryCtx)
	if err != nil {
		return err
	}

	windowEnd := time.Now().UTC().Truncate(time.Hour)
	windowStart := windowEnd.Add(-1 * time.Hour)

	result, err := c.queryBillingAggregates(
		queryCtx,
		windowStart,
		windowEnd,
		c.config.EnableOwnerMetrics,
	)
	if err != nil {
		return err
	}

	snapshot := aggregateBillingRows(
		result.Resources,
		result.ResourceAmounts,
		properties,
		c.config.EnableOwnerMetrics,
		windowStart,
		windowEnd,
	)
	snapshot.Metrics = c.buildSnapshotMetrics(snapshot)

	c.mu.Lock()
	c.snapshot = snapshot
	c.mu.Unlock()

	c.logger.WithFields(log.Fields{
		"enable_owner_metrics":   c.config.EnableOwnerMetrics,
		"resource_groups":        len(result.Resources),
		"resource_amount_groups": len(result.ResourceAmounts),
		"window_start":           snapshot.WindowStart,
		"window_end":             snapshot.WindowEnd,
		"duration":               time.Since(startedAt),
	}).Info("Billing snapshot updated")

	return nil
}

func (c *Collector) queryBillingAggregates(
	ctx context.Context,
	windowStart, windowEnd time.Time,
	enableOwnerMetrics bool,
) (*billingAggregateResult, error) {
	var (
		wg                 sync.WaitGroup
		resources          []billingAggregateRow
		resourceAmounts    []billingResourceAmountRow
		resourceErr        error
		resourceAmountsErr error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()

		resources, resourceErr = c.queryBillingResourceRows(
			ctx,
			windowStart,
			windowEnd,
			enableOwnerMetrics,
		)
	}()
	go func() {
		defer wg.Done()

		resourceAmounts, resourceAmountsErr = c.queryBillingResourceAmountRows(
			ctx,
			windowStart,
			windowEnd,
			enableOwnerMetrics,
		)
	}()

	wg.Wait()

	if resourceErr != nil {
		return nil, resourceErr
	}

	if resourceAmountsErr != nil {
		return nil, resourceAmountsErr
	}

	return &billingAggregateResult{
		Resources:       resources,
		ResourceAmounts: resourceAmounts,
	}, nil
}

func (c *Collector) queryBillingResourceRows(
	ctx context.Context,
	windowStart, windowEnd time.Time,
	enableOwnerMetrics bool,
) ([]billingAggregateRow, error) {
	pipeline := billingAggregatePipeline(
		windowStart,
		windowEnd,
		billingResourceUsageStages(enableOwnerMetrics),
	)

	cursor, err := c.mongoClient.
		Database(c.config.Mongo.Database).
		Collection(c.config.Mongo.BillingCollection).
		Aggregate(ctx, pipeline, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		return nil, fmt.Errorf("aggregate billing resource usage: %w", err)
	}
	defer cursor.Close(ctx)

	var rows []billingAggregateRow
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode billing resource usage rows: %w", err)
	}

	return rows, nil
}

func (c *Collector) queryBillingResourceAmountRows(
	ctx context.Context,
	windowStart, windowEnd time.Time,
	enableOwnerMetrics bool,
) ([]billingResourceAmountRow, error) {
	pipeline := billingAggregatePipeline(
		windowStart,
		windowEnd,
		billingResourceAmountStages(enableOwnerMetrics),
	)

	cursor, err := c.mongoClient.
		Database(c.config.Mongo.Database).
		Collection(c.config.Mongo.BillingCollection).
		Aggregate(ctx, pipeline, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		return nil, fmt.Errorf("aggregate billing resource amounts: %w", err)
	}
	defer cursor.Close(ctx)

	var rows []billingResourceAmountRow
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode billing resource amount rows: %w", err)
	}

	return rows, nil
}

func billingAggregatePipeline(
	windowStart, windowEnd time.Time,
	stages mongo.Pipeline,
) mongo.Pipeline {
	pipeline := make(mongo.Pipeline, 0, len(stages)+1)
	pipeline = append(pipeline, bson.D{{
		Key: "$match",
		Value: bson.D{
			{Key: "type", Value: billingTypeConsumption},
			{Key: "time", Value: bson.D{
				{Key: "$gt", Value: windowStart},
				{Key: "$lte", Value: windowEnd},
			}},
		},
	}})
	pipeline = append(pipeline, stages...)

	return pipeline
}

func billingResourceUsageStages(enableOwnerMetrics bool) mongo.Pipeline {
	projectFields := bson.D{{Key: "app_costs", Value: 1}}
	usedProjectFields := bson.D{{
		Key: "used",
		Value: bson.D{{
			Key: "$objectToArray",
			Value: bson.D{{
				Key: "$ifNull",
				Value: bson.A{
					"$app_costs.used",
					bson.D{},
				},
			}},
		}},
	}}
	groupID := bson.D{{Key: "resource", Value: "$used.k"}}
	projectResult := bson.D{
		{Key: "_id", Value: 0},
		{Key: "resource", Value: "$_id.resource"},
		{Key: "used", Value: 1},
	}

	if enableOwnerMetrics {
		projectFields = append(
			projectFields,
			bson.E{Key: "owner", Value: 1},
			bson.E{Key: "namespace", Value: 1},
		)
		usedProjectFields = append(
			usedProjectFields,
			bson.E{Key: "owner", Value: 1},
			bson.E{Key: "namespace", Value: 1},
		)
		groupID = append(
			bson.D{
				{Key: "owner", Value: "$owner"},
				{Key: "namespace", Value: "$namespace"},
			},
			groupID...,
		)
		projectResult = append(
			bson.D{
				{Key: "_id", Value: 0},
				{Key: "owner", Value: "$_id.owner"},
				{Key: "namespace", Value: "$_id.namespace"},
			},
			projectResult[1:]...,
		)
	}

	return mongo.Pipeline{
		bson.D{{Key: "$project", Value: projectFields}},
		bson.D{{Key: "$unwind", Value: "$app_costs"}},
		bson.D{{Key: "$project", Value: usedProjectFields}},
		bson.D{{Key: "$unwind", Value: "$used"}},
		bson.D{{
			Key: "$group",
			Value: bson.D{
				{Key: "_id", Value: groupID},
				{Key: "used", Value: bson.D{{Key: "$sum", Value: "$used.v"}}},
			},
		}},
		bson.D{{Key: "$project", Value: projectResult}},
	}
}

func billingResourceAmountStages(enableOwnerMetrics bool) mongo.Pipeline {
	projectFields := bson.D{{Key: "app_costs", Value: 1}}
	amountProjectFields := bson.D{{
		Key: "amount",
		Value: bson.D{{
			Key: "$objectToArray",
			Value: bson.D{{
				Key: "$ifNull",
				Value: bson.A{
					"$app_costs.used_amount",
					bson.D{},
				},
			}},
		}},
	}}
	groupID := bson.D{{Key: "resource", Value: "$amount.k"}}
	projectResult := bson.D{
		{Key: "_id", Value: 0},
		{Key: "resource", Value: "$_id.resource"},
		{Key: "amount", Value: 1},
	}

	if enableOwnerMetrics {
		projectFields = append(
			projectFields,
			bson.E{Key: "owner", Value: 1},
			bson.E{Key: "namespace", Value: 1},
		)
		amountProjectFields = append(
			amountProjectFields,
			bson.E{Key: "owner", Value: 1},
			bson.E{Key: "namespace", Value: 1},
		)
		groupID = append(
			bson.D{
				{Key: "owner", Value: "$owner"},
				{Key: "namespace", Value: "$namespace"},
			},
			groupID...,
		)
		projectResult = append(
			bson.D{
				{Key: "_id", Value: 0},
				{Key: "owner", Value: "$_id.owner"},
				{Key: "namespace", Value: "$_id.namespace"},
			},
			projectResult[1:]...,
		)
	}

	return mongo.Pipeline{
		bson.D{{Key: "$project", Value: projectFields}},
		bson.D{{Key: "$unwind", Value: "$app_costs"}},
		bson.D{{Key: "$project", Value: amountProjectFields}},
		bson.D{{Key: "$unwind", Value: "$amount"}},
		bson.D{{
			Key: "$group",
			Value: bson.D{
				{Key: "_id", Value: groupID},
				{Key: "amount", Value: bson.D{{Key: "$sum", Value: "$amount.v"}}},
			},
		}},
		bson.D{{Key: "$project", Value: projectResult}},
	}
}

func (c *Collector) collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	snapshot := c.snapshot
	c.mu.RUnlock()

	if snapshot == nil {
		return
	}

	if len(snapshot.Metrics) > 0 {
		for _, metric := range snapshot.Metrics {
			ch <- metric
		}

		return
	}

	c.emitSnapshotMetrics(func(metric prometheus.Metric) {
		ch <- metric
	}, snapshot)
}

func (c *Collector) buildSnapshotMetrics(snapshot *BillingSnapshot) []prometheus.Metric {
	metrics := make(
		[]prometheus.Metric,
		0,
		len(snapshot.Resources)+
			len(snapshot.OwnerResources)+
			len(snapshot.ResourceAmounts)+
			len(snapshot.OwnerResourceAmounts)+
			1,
	)

	c.emitSnapshotMetrics(func(metric prometheus.Metric) {
		metrics = append(metrics, metric)
	}, snapshot)

	return metrics
}

func (c *Collector) emitSnapshotMetrics(emit func(prometheus.Metric), snapshot *BillingSnapshot) {
	windowStart := strconv.FormatInt(snapshot.WindowStart.Unix(), 10)
	windowEnd := strconv.FormatInt(snapshot.WindowEnd.Unix(), 10)

	for key, total := range snapshot.Resources {
		emit(prometheus.MustNewConstMetric(
			c.resourceUsageGauge,
			prometheus.GaugeValue,
			total.Used,
			windowStart,
			windowEnd,
			key.Resource,
			key.Unit,
		))
	}

	if c.config.EnableOwnerMetrics {
		for key, total := range snapshot.OwnerResources {
			emit(prometheus.MustNewConstMetric(
				c.ownerResourceUsageGauge,
				prometheus.GaugeValue,
				total.Used,
				windowStart,
				windowEnd,
				key.Owner,
				key.Namespace,
				key.Resource,
				key.Unit,
			))
		}
	}

	for key, total := range snapshot.ResourceAmounts {
		emit(prometheus.MustNewConstMetric(
			c.resourceAmountGauge,
			prometheus.GaugeValue,
			total.Amount,
			windowStart,
			windowEnd,
			key.Resource,
		))
	}

	if c.config.EnableOwnerMetrics {
		for key, total := range snapshot.OwnerResourceAmounts {
			emit(prometheus.MustNewConstMetric(
				c.ownerResourceAmountGauge,
				prometheus.GaugeValue,
				total.Amount,
				windowStart,
				windowEnd,
				key.Owner,
				key.Namespace,
				key.Resource,
			))
		}
	}

	emit(prometheus.MustNewConstMetric(
		c.lastSuccessGauge,
		prometheus.GaugeValue,
		float64(snapshot.FinishedAt.Unix()),
	))
}

func aggregateBillingRows(
	rows []billingAggregateRow,
	resourceAmounts []billingResourceAmountRow,
	properties map[string]PropertyInfo,
	enableOwnerMetrics bool,
	windowStart, windowEnd time.Time,
) *BillingSnapshot {
	snapshot := newBillingSnapshot(windowStart, windowEnd)

	for _, row := range rows {
		property := propertyInfo(row.Resource, properties)
		key := resourceKey{
			Resource: property.Name,
			Unit:     property.Unit,
		}
		used := float64(int64Value(row.Used))

		total := snapshot.Resources[key]
		total.Used += used
		snapshot.Resources[key] = total

		if enableOwnerMetrics {
			ownerKey := key
			ownerKey.Owner = row.Owner
			ownerKey.Namespace = row.Namespace
			ownerTotal := snapshot.OwnerResources[ownerKey]
			ownerTotal.Used += used
			snapshot.OwnerResources[ownerKey] = ownerTotal
		}
	}

	for _, row := range resourceAmounts {
		property := propertyInfo(row.Resource, properties)
		key := resourceAmountKey{
			Resource: property.Name,
		}
		amount := float64(int64Value(row.Amount))

		total := snapshot.ResourceAmounts[key]
		total.Amount += amount
		snapshot.ResourceAmounts[key] = total

		if enableOwnerMetrics {
			ownerKey := key
			ownerKey.Owner = row.Owner
			ownerKey.Namespace = row.Namespace
			ownerTotal := snapshot.OwnerResourceAmounts[ownerKey]
			ownerTotal.Amount += amount
			snapshot.OwnerResourceAmounts[ownerKey] = ownerTotal
		}
	}

	snapshot.FinishedAt = time.Now().UTC()

	return snapshot
}

func aggregateBillingDocuments(
	docs []bson.M,
	properties map[string]PropertyInfo,
	windowStart, windowEnd time.Time,
) *BillingSnapshot {
	snapshot := newBillingSnapshot(windowStart, windowEnd)

	for _, doc := range docs {
		owner := stringValue(doc["owner"])
		namespace := stringValue(doc["namespace"])

		for _, cost := range arrayValue(doc["app_costs"]) {
			costMap, ok := cost.(bson.M)
			if !ok {
				continue
			}

			used := usedMap(costMap["used"])
			for enum, value := range used {
				property := propertyInfo(enum, properties)
				key := resourceKey{
					Resource: property.Name,
					Unit:     property.Unit,
				}
				total := snapshot.Resources[key]
				total.Used += float64(value)
				snapshot.Resources[key] = total

				ownerKey := key
				ownerKey.Owner = owner
				ownerKey.Namespace = namespace
				ownerTotal := snapshot.OwnerResources[ownerKey]
				ownerTotal.Used += float64(value)
				snapshot.OwnerResources[ownerKey] = ownerTotal
			}

			usedAmounts := usedMap(costMap["used_amount"])
			for enum, value := range usedAmounts {
				property := propertyInfo(enum, properties)
				key := resourceAmountKey{
					Resource: property.Name,
				}
				total := snapshot.ResourceAmounts[key]
				total.Amount += float64(value)
				snapshot.ResourceAmounts[key] = total

				ownerKey := key
				ownerKey.Owner = owner
				ownerKey.Namespace = namespace
				ownerTotal := snapshot.OwnerResourceAmounts[ownerKey]
				ownerTotal.Amount += float64(value)
				snapshot.OwnerResourceAmounts[ownerKey] = ownerTotal
			}
		}
	}

	snapshot.FinishedAt = time.Now().UTC()

	return snapshot
}

func newBillingSnapshot(windowStart, windowEnd time.Time) *BillingSnapshot {
	return &BillingSnapshot{
		StartedAt:            time.Now().UTC(),
		WindowStart:          windowStart,
		WindowEnd:            windowEnd,
		Resources:            make(map[resourceKey]resourceTotal),
		OwnerResources:       make(map[resourceKey]resourceTotal),
		ResourceAmounts:      make(map[resourceAmountKey]amountTotal),
		OwnerResourceAmounts: make(map[resourceAmountKey]amountTotal),
	}
}

func (c *Collector) loadProperties(ctx context.Context) (map[string]PropertyInfo, error) {
	properties := make(map[string]PropertyInfo, len(defaultProperties))
	maps.Copy(properties, defaultProperties)

	cursor, err := c.mongoClient.
		Database(c.config.Mongo.Database).
		Collection(c.config.Mongo.PropertiesCollection).
		Find(ctx, bson.M{}, optionsFindProjection(bson.M{"enum": 1, "name": 1, "unit": 1}))
	if err != nil {
		return nil, fmt.Errorf("query billing properties: %w", err)
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode billing property: %w", err)
		}

		enum := strconv.FormatInt(int64Value(doc["enum"]), 10)
		if enum == "" {
			continue
		}

		name := stringValue(doc["name"])
		if name == "" {
			continue
		}

		unit := stringValue(doc["unit"])
		if unit == "" {
			unit = "1"
		}

		properties[enum] = PropertyInfo{Name: name, Unit: unit}
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate billing properties: %w", err)
	}

	return properties, nil
}
