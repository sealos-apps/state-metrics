package crds

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/labring/sealos-state-metrics/pkg/collector/crds/helpers"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	defaultMetricPrefix = "sealos"
	defaultValueLabel   = "value"
	defaultKeyLabel     = "key"
	defaultStateLabel   = "state"
)

type metricStore struct {
	mu sync.RWMutex

	commonLabelPaths []string
	metrics          []compiledMetric
	descriptors      map[string]*prometheus.Desc
	namespaces       map[string]struct{}

	objects      map[string]objectMetrics
	countValues  map[string]map[string]int
	countMetrics map[string]map[string]prometheus.Metric

	logger *log.Entry
	stats  metricStoreStats
}

type compiledMetric struct {
	config     MetricConfig
	desc       *prometheus.Desc
	labelPaths []string

	typeField   string
	statusField string
	reasonField string
}

type objectMetrics struct {
	series []prometheus.Metric
	counts []countContribution
}

type countContribution struct {
	metricName string
	value      string
}

type metricStoreStats struct {
	AddCount    atomic.Int64
	UpdateCount atomic.Int64
	DeleteCount atomic.Int64
}

type metricStoreStatsSnapshot struct {
	AddCount    int64
	UpdateCount int64
	DeleteCount int64
	TotalCount  int64
}

func newMetricStore(
	crdConfig *CRDConfig,
	metricPrefix string,
	logger *log.Entry,
) (*metricStore, error) {
	if crdConfig == nil {
		return nil, errors.New("crd config cannot be nil")
	}

	if metricPrefix == "" {
		metricPrefix = defaultMetricPrefix
	}

	if logger == nil {
		logger = log.WithField("component", "crd_metric_store")
	}

	store := &metricStore{
		commonLabelPaths: helpers.GetSortedValues(crdConfig.CommonLabels),
		descriptors:      make(map[string]*prometheus.Desc),
		namespaces:       makeNamespaceSet(crdConfig.Namespaces),
		objects:          make(map[string]objectMetrics),
		countValues:      make(map[string]map[string]int),
		countMetrics:     make(map[string]map[string]prometheus.Metric),
		logger:           logger.WithField("crd", crdConfig.Name),
	}

	commonLabelNames := helpers.GetSortedKeys(crdConfig.CommonLabels)
	for _, metricCfg := range crdConfig.Metrics {
		compiled, ok := compileMetric(metricPrefix, commonLabelNames, metricCfg)
		if !ok {
			store.logger.WithField("type", metricCfg.Type).Warn("Unknown metric type, skipping")
			continue
		}

		store.metrics = append(store.metrics, compiled)
		store.descriptors[metricCfg.Name] = compiled.desc
	}

	store.logger.WithFields(log.Fields{
		"metric_count": len(store.metrics),
		"namespace":    len(store.namespaces),
	}).Info("CRD metric store initialized")

	return store, nil
}

func compileMetric(
	metricPrefix string,
	commonLabelNames []string,
	metricCfg MetricConfig,
) (compiledMetric, bool) {
	labelNames := make([]string, 0, len(commonLabelNames)+len(metricCfg.Labels)+3)
	labelNames = append(labelNames, commonLabelNames...)

	compiled := compiledMetric{config: metricCfg}

	switch metricCfg.Type {
	case "info":
		labelNames = append(labelNames, helpers.GetSortedKeys(metricCfg.Labels)...)
		compiled.labelPaths = helpers.GetSortedValues(metricCfg.Labels)
	case "gauge":
	case "count":
		labelNames = make([]string, 1, len(commonLabelNames)+len(metricCfg.Labels)+3)
		labelNames[0] = labelOrDefault(metricCfg.ValueLabel, defaultValueLabel)
	case "string_state":
		labelNames = append(labelNames, labelOrDefault(metricCfg.ValueLabel, defaultStateLabel))
	case "map_state":
		labelNames = append(
			labelNames,
			labelOrDefault(metricCfg.KeyLabel, defaultKeyLabel),
			labelOrDefault(metricCfg.ValueLabel, defaultStateLabel),
		)
	case "map_gauge":
		labelNames = append(labelNames, labelOrDefault(metricCfg.KeyLabel, defaultKeyLabel))
	case "conditions":
		labelNames = append(labelNames, "type", "status", "reason")
		compiled.typeField = "type"
		compiled.statusField = "status"

		compiled.reasonField = "reason"
		if metricCfg.Condition != nil {
			if metricCfg.Condition.TypeField != "" {
				compiled.typeField = metricCfg.Condition.TypeField
			}

			if metricCfg.Condition.StatusField != "" {
				compiled.statusField = metricCfg.Condition.StatusField
			}

			if metricCfg.Condition.ReasonField != "" {
				compiled.reasonField = metricCfg.Condition.ReasonField
			}
		}
	default:
		return compiledMetric{}, false
	}

	compiled.desc = prometheus.NewDesc(
		prometheus.BuildFQName(metricPrefix, "", metricCfg.Name),
		metricCfg.Help,
		labelNames,
		nil,
	)

	return compiled, true
}

func (s *metricStore) Add(obj *unstructured.Unstructured) {
	s.upsert(obj)
}

func (s *metricStore) Update(obj *unstructured.Unstructured) {
	s.upsert(obj)
}

func (s *metricStore) Delete(obj *unstructured.Unstructured) {
	if obj == nil {
		s.logger.Warn("Attempted to delete nil object")
		return
	}

	key := resourceKey(obj)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.objects[key]; !ok {
		return
	}

	s.removeLocked(key)
	s.stats.DeleteCount.Add(1)
}

func (s *metricStore) Collect(ch chan<- prometheus.Metric) {
	metrics := s.snapshotMetrics()
	for _, metric := range metrics {
		ch <- metric
	}
}

func (s *metricStore) snapshotMetrics() []prometheus.Metric {
	s.mu.RLock()
	defer s.mu.RUnlock()

	metrics := make([]prometheus.Metric, 0, s.metricCountLocked())
	for _, objectMetrics := range s.objects {
		metrics = append(metrics, objectMetrics.series...)
	}

	for _, metricsByValue := range s.countMetrics {
		for _, metric := range metricsByValue {
			metrics = append(metrics, metric)
		}
	}

	return metrics
}

func (s *metricStore) metricCountLocked() int {
	count := 0
	for _, objectMetrics := range s.objects {
		count += len(objectMetrics.series)
	}

	for _, metricsByValue := range s.countMetrics {
		count += len(metricsByValue)
	}

	return count
}

func (s *metricStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.objects)
}

func (s *metricStore) Descriptors() map[string]*prometheus.Desc {
	descriptors := make(map[string]*prometheus.Desc, len(s.descriptors))
	maps.Copy(descriptors, s.descriptors)

	return descriptors
}

func (s *metricStore) GetMetrics() metricStoreStatsSnapshot {
	return metricStoreStatsSnapshot{
		AddCount:    s.stats.AddCount.Load(),
		UpdateCount: s.stats.UpdateCount.Load(),
		DeleteCount: s.stats.DeleteCount.Load(),
		TotalCount:  int64(s.Len()),
	}
}

func (s *metricStore) RequiredPaths() []string {
	seen := make(map[string]struct{})
	paths := make([]string, 0)

	addPath := func(path string) {
		if path == "" {
			return
		}

		if _, ok := seen[path]; ok {
			return
		}

		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	for _, path := range s.commonLabelPaths {
		addPath(path)
	}

	for _, metric := range s.metrics {
		addPath(metric.config.Path)

		for _, path := range metric.labelPaths {
			addPath(path)
		}
	}

	return paths
}

func (s *metricStore) upsert(obj *unstructured.Unstructured) {
	if obj == nil {
		s.logger.Warn("Attempted to upsert nil object")
		return
	}

	key := resourceKey(obj)
	newMetrics, ok := s.buildObjectMetrics(obj)

	s.mu.Lock()
	defer s.mu.Unlock()

	_, exists := s.objects[key]
	if exists {
		s.removeLocked(key)
	}

	if !ok {
		if exists {
			s.stats.DeleteCount.Add(1)
		}
		return
	}

	s.objects[key] = newMetrics
	for _, contribution := range newMetrics.counts {
		s.addCountLocked(contribution.metricName, contribution.value)
	}

	if exists {
		s.stats.UpdateCount.Add(1)
	} else {
		s.stats.AddCount.Add(1)
	}
}

func (s *metricStore) buildObjectMetrics(obj *unstructured.Unstructured) (objectMetrics, bool) {
	if !s.namespaceAllowed(obj.GetNamespace()) {
		return objectMetrics{}, false
	}

	commonLabels := s.extractCommonLabels(obj)
	result := objectMetrics{
		series: make([]prometheus.Metric, 0, len(s.metrics)),
		counts: make([]countContribution, 0),
	}

	for _, metric := range s.metrics {
		switch metric.config.Type {
		case "info":
			result.series = append(result.series, s.buildInfoMetric(obj, metric, commonLabels))
		case "gauge":
			result.series = append(result.series, s.buildGaugeMetric(obj, metric, commonLabels))
		case "count":
			value := helpers.ExtractFieldString(obj, metric.config.Path)
			if value != "" {
				result.counts = append(result.counts, countContribution{
					metricName: metric.config.Name,
					value:      value,
				})
			}
		case "string_state":
			result.series = append(
				result.series,
				s.buildStringStateMetric(obj, metric, commonLabels),
			)
		case "map_state":
			result.series = append(
				result.series,
				s.buildMapStateMetrics(obj, metric, commonLabels)...)
		case "map_gauge":
			result.series = append(
				result.series,
				s.buildMapGaugeMetrics(obj, metric, commonLabels)...)
		case "conditions":
			result.series = append(
				result.series,
				s.buildConditionMetrics(obj, metric, commonLabels)...)
		}
	}

	return result, true
}

func (s *metricStore) buildInfoMetric(
	obj *unstructured.Unstructured,
	metric compiledMetric,
	commonLabels []string,
) prometheus.Metric {
	labels := make([]string, len(commonLabels), len(commonLabels)+len(metric.labelPaths))
	copy(labels, commonLabels)

	for _, path := range metric.labelPaths {
		labels = append(labels, helpers.ExtractFieldString(obj, path))
	}

	return prometheus.MustNewConstMetric(metric.desc, prometheus.GaugeValue, 1, labels...)
}

func (s *metricStore) buildGaugeMetric(
	obj *unstructured.Unstructured,
	metric compiledMetric,
	commonLabels []string,
) prometheus.Metric {
	value := helpers.ExtractFieldFloat(obj, metric.config.Path)
	return prometheus.MustNewConstMetric(metric.desc, prometheus.GaugeValue, value, commonLabels...)
}

func (s *metricStore) buildStringStateMetric(
	obj *unstructured.Unstructured,
	metric compiledMetric,
	commonLabels []string,
) prometheus.Metric {
	labels := make([]string, len(commonLabels), len(commonLabels)+1)
	copy(labels, commonLabels)
	labels = append(labels, helpers.ExtractFieldString(obj, metric.config.Path))

	return prometheus.MustNewConstMetric(metric.desc, prometheus.GaugeValue, 1, labels...)
}

func (s *metricStore) buildMapStateMetrics(
	obj *unstructured.Unstructured,
	metric compiledMetric,
	commonLabels []string,
) []prometheus.Metric {
	mapData := helpers.ExtractFieldMap(obj, metric.config.Path)
	result := make([]prometheus.Metric, 0, len(mapData))

	for key, entryData := range mapData {
		entryMap, ok := entryData.(map[string]any)
		if !ok {
			continue
		}

		currentState, _ := entryMap[metric.config.ValuePath].(string)
		if currentState == "" {
			continue
		}

		labels := make([]string, len(commonLabels), len(commonLabels)+2)
		copy(labels, commonLabels)
		labels = append(labels, key, currentState)

		result = append(result, prometheus.MustNewConstMetric(
			metric.desc,
			prometheus.GaugeValue,
			1,
			labels...,
		))
	}

	return result
}

func (s *metricStore) buildMapGaugeMetrics(
	obj *unstructured.Unstructured,
	metric compiledMetric,
	commonLabels []string,
) []prometheus.Metric {
	mapData := helpers.ExtractFieldMap(obj, metric.config.Path)
	result := make([]prometheus.Metric, 0, len(mapData))

	for key, entryData := range mapData {
		entryMap, ok := entryData.(map[string]any)
		if !ok {
			continue
		}

		value := 0.0
		if rawValue, ok := entryMap[metric.config.ValuePath]; ok {
			value = helpers.ToFloat64(rawValue)
		}

		labels := make([]string, len(commonLabels), len(commonLabels)+1)
		copy(labels, commonLabels)
		labels = append(labels, key)

		result = append(result, prometheus.MustNewConstMetric(
			metric.desc,
			prometheus.GaugeValue,
			value,
			labels...,
		))
	}

	return result
}

func (s *metricStore) buildConditionMetrics(
	obj *unstructured.Unstructured,
	metric compiledMetric,
	commonLabels []string,
) []prometheus.Metric {
	conditions := helpers.ExtractFieldSlice(obj, metric.config.Path)
	result := make([]prometheus.Metric, 0, len(conditions))

	for _, condData := range conditions {
		condMap, ok := condData.(map[string]any)
		if !ok {
			continue
		}

		condType, _ := condMap[metric.typeField].(string)
		condStatus, _ := condMap[metric.statusField].(string)
		condReason, _ := condMap[metric.reasonField].(string)

		if condType == "" {
			continue
		}

		value := 0.0
		if strings.EqualFold(condStatus, "true") {
			value = 1.0
		}

		labels := make([]string, len(commonLabels), len(commonLabels)+3)
		copy(labels, commonLabels)
		labels = append(labels, condType, condStatus, condReason)

		result = append(result, prometheus.MustNewConstMetric(
			metric.desc,
			prometheus.GaugeValue,
			value,
			labels...,
		))
	}

	return result
}

func (s *metricStore) extractCommonLabels(obj *unstructured.Unstructured) []string {
	labels := make([]string, 0, len(s.commonLabelPaths))
	for _, path := range s.commonLabelPaths {
		labels = append(labels, helpers.ExtractFieldString(obj, path))
	}

	return labels
}

func (s *metricStore) removeLocked(key string) {
	metrics := s.objects[key]
	for _, contribution := range metrics.counts {
		s.removeCountLocked(contribution.metricName, contribution.value)
	}

	delete(s.objects, key)
}

func (s *metricStore) addCountLocked(metricName, value string) {
	if s.countValues[metricName] == nil {
		s.countValues[metricName] = make(map[string]int)
	}

	if s.countMetrics[metricName] == nil {
		s.countMetrics[metricName] = make(map[string]prometheus.Metric)
	}

	s.countValues[metricName][value]++
	s.refreshCountMetricLocked(metricName, value)
}

func (s *metricStore) removeCountLocked(metricName, value string) {
	values := s.countValues[metricName]
	if values == nil {
		return
	}

	values[value]--
	if values[value] <= 0 {
		delete(values, value)
		delete(s.countMetrics[metricName], value)
		return
	}

	s.refreshCountMetricLocked(metricName, value)
}

func (s *metricStore) refreshCountMetricLocked(metricName, value string) {
	desc := s.descriptors[metricName]
	if desc == nil {
		return
	}

	count := s.countValues[metricName][value]
	s.countMetrics[metricName][value] = prometheus.MustNewConstMetric(
		desc,
		prometheus.GaugeValue,
		float64(count),
		value,
	)
}

func (s *metricStore) namespaceAllowed(namespace string) bool {
	if len(s.namespaces) == 0 {
		return true
	}

	_, ok := s.namespaces[namespace]

	return ok
}

func resourceKey(obj *unstructured.Unstructured) string {
	namespace := obj.GetNamespace()

	name := obj.GetName()
	if namespace == "" {
		return name
	}

	return fmt.Sprintf("%s/%s", namespace, name)
}

func makeNamespaceSet(namespaces []string) map[string]struct{} {
	result := make(map[string]struct{}, len(namespaces))
	for _, namespace := range namespaces {
		if namespace == "" {
			continue
		}

		result[namespace] = struct{}{}
	}

	return result
}

func labelOrDefault(label, defaultLabel string) string {
	if label == "" {
		return defaultLabel
	}

	return label
}
