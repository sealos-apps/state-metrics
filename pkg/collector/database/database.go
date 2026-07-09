package database

import (
	"context"
	"sync"
	"time"

	"github.com/labring/sealos-state-metrics/pkg/collector/base"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DatabaseType represents the type of database
type DatabaseType string

const (
	DatabaseTypeMySQL      DatabaseType = "mysql"
	DatabaseTypePostgreSQL DatabaseType = "postgresql"
	DatabaseTypeMongoDB    DatabaseType = "mongodb"
	DatabaseTypeRedis      DatabaseType = "redis"
)

// DatabaseStatus holds the connectivity status of a database
type DatabaseStatus struct {
	Name         string
	Namespace    string
	DatabaseType DatabaseType
	Connected    bool
	LastCheck    time.Time
	Error        string
	ResponseTime float64 // in seconds
}

// DatabaseTask represents a database connection check task
type DatabaseTask struct {
	Namespace    string
	DatabaseName string
	DatabaseType DatabaseType
	Secret       *corev1.Secret
}

// Collector implements database connectivity monitoring
type Collector struct {
	*base.BaseCollector
	config *Config
	logger *log.Entry
	client kubernetes.Interface

	// Prometheus metrics
	connectivityGauge *prometheus.Desc
	responseTimeGauge *prometheus.Desc
	// Statistics metrics
	totalDatabasesGauge       *prometheus.Desc
	availableDatabasesGauge   *prometheus.Desc
	unavailableDatabasesGauge *prometheus.Desc

	// Internal state
	mu             sync.RWMutex
	dbConnectivity map[string]*DatabaseStatus // key: namespace/name
	// Statistics
	stats struct {
		total       int
		available   int
		unavailable int
	}

	// Secret cache and preflight checker
	secretCache      *SecretCache
	preflightChecker *PreflightChecker
}

// initMetrics initializes Prometheus metric descriptors
func (c *Collector) initMetrics(namespace string) {
	c.connectivityGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "database", "connectivity"),
		"Database connectivity status (1 = connected, 0 = disconnected)",
		[]string{"namespace", "database", "type"}, // namespace is K8s namespace label
		nil,
	)

	c.responseTimeGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "database", "response_time_seconds"),
		"Database connection response time in seconds",
		[]string{"namespace", "database", "type"}, // namespace is K8s namespace label
		nil,
	)

	c.totalDatabasesGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "database", "total"),
		"Total number of databases being monitored",
		[]string{"type"}, // database type
		nil,
	)

	c.availableDatabasesGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "database", "available"),
		"Number of available (connected) databases",
		[]string{"type"}, // database type
		nil,
	)

	c.unavailableDatabasesGauge = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "database", "unavailable"),
		"Number of unavailable (disconnected) databases",
		[]string{"type"}, // database type
		nil,
	)

	c.MustRegisterDesc(c.connectivityGauge)
	c.MustRegisterDesc(c.responseTimeGauge)
	c.MustRegisterDesc(c.totalDatabasesGauge)
	c.MustRegisterDesc(c.availableDatabasesGauge)
	c.MustRegisterDesc(c.unavailableDatabasesGauge)
}

// HasSynced returns true (polling collector is always synced)
func (c *Collector) HasSynced() bool {
	return true
}

// Interval returns the polling interval
func (c *Collector) Interval() time.Duration {
	return c.config.CheckInterval
}

// pollLoop periodically checks database connectivity
func (c *Collector) pollLoop(ctx context.Context) {
	// Initialize secret cache and preflight checker
	c.secretCache = NewSecretCache(c.client, c.logger)
	c.preflightChecker = NewPreflightChecker(c.client, c.logger)

	// Start secret cache
	if err := c.secretCache.Start(ctx, c.config.Namespaces); err != nil {
		c.logger.WithError(err).
			Error("Failed to start secret cache, falling back to direct queries")
		c.secretCache = nil // Disable cache on error
	} else {
		c.logger.WithField("cache_size", c.secretCache.GetCacheSize()).
			Info("Secret cache started successfully")
	}

	c.RunPollLoop(ctx, c.Poll, base.PollLoopOptions{
		Interval:  c.config.CheckInterval,
		Operation: "database",
	})

	if c.secretCache != nil {
		c.secretCache.Stop()
	}
}

// Poll checks all databases in the cluster
func (c *Collector) Poll(ctx context.Context) error {
	c.logger.Debug("Starting database connectivity checks")

	// Collect all database tasks
	tasks := c.collectDatabaseTasks(ctx)

	c.logger.WithField("total_tasks", len(tasks)).Debug("Collected database tasks")

	// Process tasks concurrently by database type
	newStatus := c.processDatabaseTasksConcurrently(ctx, tasks)

	// Calculate statistics
	stats := c.calculateStatistics(newStatus)

	// Update internal state
	c.mu.Lock()
	c.dbConnectivity = newStatus
	c.stats.total = stats.total
	c.stats.available = stats.available
	c.stats.unavailable = stats.unavailable
	c.mu.Unlock()

	c.logger.WithFields(log.Fields{
		"total":       stats.total,
		"available":   stats.available,
		"unavailable": stats.unavailable,
	}).Info("Database connectivity check completed")

	return nil
}

// collectDatabaseTasks collects all database connection tasks across namespaces
func (c *Collector) collectDatabaseTasks(ctx context.Context) []DatabaseTask {
	var tasks []DatabaseTask

	// Check if we need to scan all namespaces or specific ones
	if len(c.config.Namespaces) == 0 {
		// Scan all namespaces: fetch all secrets at once for better performance
		allTasks := c.collectAllNamespaceTasks(ctx)
		tasks = append(tasks, allTasks...)
	} else {
		// Scan specific namespaces
		for _, ns := range c.config.Namespaces {
			nsTasks := c.collectNamespaceTasks(ctx, ns)
			tasks = append(tasks, nsTasks...)
		}
	}

	return tasks
}

// collectAllNamespaceTasks collects tasks from all namespaces efficiently
func (c *Collector) collectAllNamespaceTasks(ctx context.Context) []DatabaseTask {
	var tasks []DatabaseTask

	// Define all database types
	dbTypes := []DatabaseType{
		DatabaseTypeMySQL,
		DatabaseTypePostgreSQL,
		DatabaseTypeMongoDB,
		DatabaseTypeRedis,
	}

	// Use cache if available
	if c.secretCache != nil {
		c.logger.Debug("Using secret cache to collect tasks")

		for _, dbType := range dbTypes {
			secrets := c.secretCache.GetSecrets(dbType, c.isCredentialSecret)

			c.logger.WithFields(log.Fields{
				"type":  dbType,
				"count": len(secrets),
			}).Debug("Retrieved secrets from cache")

			for _, secret := range secrets {
				dbName := c.extractDatabaseName(secret, dbType)
				tasks = append(tasks, DatabaseTask{
					Namespace:    secret.Namespace,
					DatabaseName: dbName,
					DatabaseType: dbType,
					Secret:       secret,
				})
			}
		}

		return tasks
	}

	// Fallback to direct API calls if cache is not available
	c.logger.Debug("Cache not available, using direct API calls")

	dbTypeSelectors := map[DatabaseType]string{
		DatabaseTypeMySQL:      "apps.kubeblocks.io/cluster-type=mysql",
		DatabaseTypePostgreSQL: "app.kubernetes.io/name=postgresql",
		DatabaseTypeMongoDB:    "apps.kubeblocks.io/cluster-type=mongodb",
		DatabaseTypeRedis:      "apps.kubeblocks.io/cluster-type=redis",
	}

	// Fetch all secrets for each database type across all namespaces
	for dbType, selector := range dbTypeSelectors {
		secrets, err := c.client.CoreV1().Secrets("").List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		})
		if err != nil {
			c.logger.WithError(err).WithField("type", dbType).
				Error("Failed to list secrets for database type")
			continue
		}

		c.logger.WithFields(log.Fields{
			"type":  dbType,
			"count": len(secrets.Items),
		}).Debug("Found secrets for database type")

		// Process each secret
		for i := range secrets.Items {
			secret := &secrets.Items[i]

			// Check if this is a credential secret
			if !c.isCredentialSecret(secret, dbType) {
				continue
			}

			// Extract database name
			dbName := c.extractDatabaseName(secret, dbType)

			tasks = append(tasks, DatabaseTask{
				Namespace:    secret.Namespace,
				DatabaseName: dbName,
				DatabaseType: dbType,
				Secret:       secret,
			})
		}
	}

	return tasks
}

// collectNamespaceTasks collects tasks from a specific namespace
func (c *Collector) collectNamespaceTasks(
	ctx context.Context,
	namespace string,
) []DatabaseTask {
	var tasks []DatabaseTask

	dbTypeSelectors := map[DatabaseType]string{
		DatabaseTypeMySQL:      "apps.kubeblocks.io/cluster-type=mysql",
		DatabaseTypePostgreSQL: "app.kubernetes.io/name=postgresql",
		DatabaseTypeMongoDB:    "apps.kubeblocks.io/cluster-type=mongodb",
		DatabaseTypeRedis:      "apps.kubeblocks.io/cluster-type=redis",
	}

	for dbType, selector := range dbTypeSelectors {
		secrets, err := c.client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		})
		if err != nil {
			c.logger.WithError(err).WithFields(log.Fields{
				"namespace": namespace,
				"type":      dbType,
			}).Error("Failed to list secrets")

			continue
		}

		for i := range secrets.Items {
			secret := &secrets.Items[i]

			if !c.isCredentialSecret(secret, dbType) {
				continue
			}

			dbName := c.extractDatabaseName(secret, dbType)

			tasks = append(tasks, DatabaseTask{
				Namespace:    secret.Namespace,
				DatabaseName: dbName,
				DatabaseType: dbType,
				Secret:       secret,
			})
		}
	}

	return tasks
}

// processDatabaseTasksConcurrently processes database tasks with controlled concurrency per type
func (c *Collector) processDatabaseTasksConcurrently(
	ctx context.Context,
	tasks []DatabaseTask,
) map[string]*DatabaseStatus {
	// Group tasks by database type
	tasksByType := make(map[DatabaseType][]DatabaseTask)
	for _, task := range tasks {
		tasksByType[task.DatabaseType] = append(tasksByType[task.DatabaseType], task)
	}

	// Process each type concurrently with type-specific concurrency limits
	var wg sync.WaitGroup

	statusChan := make(chan *DatabaseStatus, len(tasks))

	for dbType, typeTasks := range tasksByType {
		wg.Add(1)

		go func(dt DatabaseType, tt []DatabaseTask) {
			defer wg.Done()

			c.processTasksWithConcurrency(ctx, dt, tt, statusChan)
		}(dbType, typeTasks)
	}

	// Wait for all processing to complete
	go func() {
		wg.Wait()
		close(statusChan)
	}()

	// Collect results
	statusMap := make(map[string]*DatabaseStatus)
	for status := range statusChan {
		key := status.Namespace + "/" + status.Name
		statusMap[key] = status
	}

	return statusMap
}

// processTasksWithConcurrency processes tasks for a specific database type with concurrency control
func (c *Collector) processTasksWithConcurrency(
	ctx context.Context,
	dbType DatabaseType,
	tasks []DatabaseTask,
	statusChan chan<- *DatabaseStatus,
) {
	// Get concurrency limit for this database type
	concurrency := c.getConcurrencyLimit(dbType)

	// Create semaphore for concurrency control
	sem := make(chan struct{}, concurrency)

	var wg sync.WaitGroup

	for _, task := range tasks {
		select {
		case <-ctx.Done():
			return
		default:
		}

		wg.Add(1)

		go func(t DatabaseTask) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Check database connectivity
			status := c.checkDatabaseConnectivity(
				ctx,
				t.Namespace,
				t.DatabaseName,
				t.DatabaseType,
				t.Secret,
			)

			if status.Connected {
				c.logger.WithFields(log.Fields{
					"namespace": t.Namespace,
					"database":  t.DatabaseName,
					"type":      t.DatabaseType,
				}).Debug("Database is healthy")
			} else {
				c.logger.WithFields(log.Fields{
					"namespace": t.Namespace,
					"database":  t.DatabaseName,
					"type":      t.DatabaseType,
					"error":     status.Error,
				}).Warn("Database is unhealthy")
			}

			statusChan <- status
		}(task)
	}

	wg.Wait()
}

// getConcurrencyLimit returns the concurrency limit for a database type
func (c *Collector) getConcurrencyLimit(dbType DatabaseType) int {
	switch dbType {
	case DatabaseTypeMySQL:
		return c.config.MySQLConcurrency
	case DatabaseTypePostgreSQL:
		return c.config.PostgreSQLConcurrency
	case DatabaseTypeMongoDB:
		return c.config.MongoDBConcurrency
	case DatabaseTypeRedis:
		return c.config.RedisConcurrency
	default:
		return 10 // fallback
	}
}

// calculateStatistics calculates overall statistics from status map
func (c *Collector) calculateStatistics(statusMap map[string]*DatabaseStatus) struct {
	total       int
	available   int
	unavailable int
} {
	stats := struct {
		total       int
		available   int
		unavailable int
	}{}

	stats.total = len(statusMap)
	for _, status := range statusMap {
		if status.Connected {
			stats.available++
		} else {
			stats.unavailable++
		}
	}

	return stats
}

// isCredentialSecret checks if the secret is a credential secret for the database type
func (c *Collector) isCredentialSecret(secret *corev1.Secret, dbType DatabaseType) bool {
	switch dbType {
	case DatabaseTypeMySQL, DatabaseTypePostgreSQL, DatabaseTypeMongoDB, DatabaseTypeRedis:
		suffix := "-conn-credential"
		return len(secret.Name) > len(suffix) &&
			secret.Name[len(secret.Name)-len(suffix):] == suffix
	}

	return false
}

// extractDatabaseName extracts the database name from the secret name
func (c *Collector) extractDatabaseName(secret *corev1.Secret, dbType DatabaseType) string {
	switch dbType {
	case DatabaseTypeMySQL, DatabaseTypePostgreSQL, DatabaseTypeMongoDB, DatabaseTypeRedis:
		// Remove "-conn-credential" suffix
		suffix := "-conn-credential"
		if len(secret.Name) > len(suffix) {
			return secret.Name[:len(secret.Name)-len(suffix)]
		}
	}

	return secret.Name
}

// checkDatabaseConnectivity checks if a database is accessible
func (c *Collector) checkDatabaseConnectivity(
	ctx context.Context,
	namespace, dbName string,
	dbType DatabaseType,
	secret *corev1.Secret,
) *DatabaseStatus {
	status := &DatabaseStatus{
		Name:         dbName,
		Namespace:    namespace,
		DatabaseType: dbType,
		LastCheck:    time.Now(),
		Connected:    false,
	}

	startTime := time.Now()

	// Preflight checks (fast-fail)
	if c.preflightChecker != nil {
		if preflightErr := c.preflightChecker.CheckDatabase(
			ctx,
			namespace,
			dbName,
			dbType,
			secret,
		); preflightErr != nil {
			status.ResponseTime = time.Since(startTime).Seconds()
			status.Connected = false
			status.Error = preflightErr.Error()
			c.logger.WithFields(log.Fields{
				"namespace": namespace,
				"database":  dbName,
				"type":      dbType,
				"errorType": preflightErr.Type,
				"error":     preflightErr.Message,
			}).Warn("Database preflight check failed (fast-fail)")

			return status
		}
	}

	// Create context with timeout for actual connection check
	checkCtx, cancel := context.WithTimeout(ctx, c.config.CheckTimeout)
	defer cancel()

	var err error
	switch dbType {
	case DatabaseTypeMySQL:
		err = c.checkMySQLConnectivity(checkCtx, secret)
	case DatabaseTypePostgreSQL:
		err = c.checkPostgreSQLConnectivity(checkCtx, namespace, secret)
	case DatabaseTypeMongoDB:
		err = c.checkMongoDBConnectivity(checkCtx, namespace, secret)
	case DatabaseTypeRedis:
		err = c.checkRedisConnectivity(checkCtx, namespace, secret)
	default:
		err = nil
	}

	status.ResponseTime = time.Since(startTime).Seconds()

	if err != nil {
		status.Connected = false
		status.Error = err.Error()
		c.logger.WithFields(log.Fields{
			"namespace":     namespace,
			"database":      dbName,
			"type":          dbType,
			"response_time": status.ResponseTime,
		}).WithError(err).Warn("Database connectivity check failed")
	} else {
		status.Connected = true
		status.Error = ""
		c.logger.WithFields(log.Fields{
			"namespace":     namespace,
			"database":      dbName,
			"type":          dbType,
			"response_time": status.ResponseTime,
		}).Debug("Database connectivity check succeeded")
	}

	return status
}

// collect implements the collect method for Prometheus
func (c *Collector) collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Per-database metrics
	for _, status := range c.dbConnectivity {
		// Connectivity metric
		connectivityValue := 0.0
		if status.Connected {
			connectivityValue = 1.0
		}

		ch <- prometheus.MustNewConstMetric(
			c.connectivityGauge,
			prometheus.GaugeValue,
			connectivityValue,
			status.Namespace,
			status.Name,
			string(status.DatabaseType),
		)

		// Response time metric
		ch <- prometheus.MustNewConstMetric(
			c.responseTimeGauge,
			prometheus.GaugeValue,
			status.ResponseTime,
			status.Namespace,
			status.Name,
			string(status.DatabaseType),
		)
	}

	// Aggregate statistics by database type
	typeStats := c.calculateTypeStatistics()

	// Emit statistics metrics for each database type
	for dbType, stats := range typeStats {
		ch <- prometheus.MustNewConstMetric(
			c.totalDatabasesGauge,
			prometheus.GaugeValue,
			float64(stats.total),
			string(dbType),
		)

		ch <- prometheus.MustNewConstMetric(
			c.availableDatabasesGauge,
			prometheus.GaugeValue,
			float64(stats.available),
			string(dbType),
		)

		ch <- prometheus.MustNewConstMetric(
			c.unavailableDatabasesGauge,
			prometheus.GaugeValue,
			float64(stats.unavailable),
			string(dbType),
		)
	}

	// Also emit overall totals with type="all"
	ch <- prometheus.MustNewConstMetric(
		c.totalDatabasesGauge,
		prometheus.GaugeValue,
		float64(c.stats.total),
		"all",
	)

	ch <- prometheus.MustNewConstMetric(
		c.availableDatabasesGauge,
		prometheus.GaugeValue,
		float64(c.stats.available),
		"all",
	)

	ch <- prometheus.MustNewConstMetric(
		c.unavailableDatabasesGauge,
		prometheus.GaugeValue,
		float64(c.stats.unavailable),
		"all",
	)
}

// calculateTypeStatistics calculates statistics per database type
func (c *Collector) calculateTypeStatistics() map[DatabaseType]struct {
	total       int
	available   int
	unavailable int
} {
	typeStats := make(map[DatabaseType]struct {
		total       int
		available   int
		unavailable int
	})

	for _, status := range c.dbConnectivity {
		stats := typeStats[status.DatabaseType]

		stats.total++
		if status.Connected {
			stats.available++
		} else {
			stats.unavailable++
		}

		typeStats[status.DatabaseType] = stats
	}

	return typeStats
}
