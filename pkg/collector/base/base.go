package base

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
)

// BaseCollector provides common functionality for all collectors.
// It implements the basic lifecycle management and state tracking.
type BaseCollector struct {
	name                   string
	requiresLeaderElection bool
	logger                 *log.Entry
	waitReadyOnCollect     bool          // If true, Collect will wait for collector to be ready
	waitReadyTimeout       time.Duration // Timeout for WaitReady in Collect

	mu        sync.RWMutex
	started   bool
	ready     bool
	readyCh   chan struct{} // closed when ready, recreated on Start
	stoppedCh chan struct{} // closed when stopped, for WaitReady to detect stop
	//nolint:containedctx // Context is intentionally stored to manage collector lifecycle between Start/Stop
	ctx    context.Context
	cancel context.CancelFunc

	// Metrics registry
	descs []*prometheus.Desc

	// Lifecycle implementation
	lifecycle Lifecycle
}

// PollFunc executes one polling cycle.
type PollFunc func(ctx context.Context) error

// PollLoopOptions configures the common background poll loop.
type PollLoopOptions struct {
	Interval       time.Duration
	Operation      string
	SlowThreshold  time.Duration
	SuccessInfoLog bool
}

// BaseCollectorOption is a functional option for configuring BaseCollector
type BaseCollectorOption func(*BaseCollector)

// WithLeaderElection returns an option that sets whether this collector requires leader election.
func WithLeaderElection(required bool) BaseCollectorOption {
	return func(b *BaseCollector) {
		b.requiresLeaderElection = required
	}
}

// WithWaitReadyOnCollect returns an option that sets whether Collect should wait for the collector to be ready.
// If enabled, Collect will call WaitReady with the configured timeout before collecting metrics.
func WithWaitReadyOnCollect(wait bool) BaseCollectorOption {
	return func(b *BaseCollector) {
		b.waitReadyOnCollect = wait
	}
}

// WithWaitReadyTimeout returns an option that sets the timeout for WaitReady in Collect.
// Default is 5 seconds. Only applies if waitReadyOnCollect is enabled.
func WithWaitReadyTimeout(timeout time.Duration) BaseCollectorOption {
	return func(b *BaseCollector) {
		b.waitReadyTimeout = timeout
	}
}

// NewBaseCollector creates a new BaseCollector instance with functional options.
func NewBaseCollector(
	name string,
	logger *log.Entry,
	opts ...BaseCollectorOption,
) *BaseCollector {
	b := &BaseCollector{
		name:                   name,
		requiresLeaderElection: true, // Default: require leader election
		logger:                 logger,
		waitReadyOnCollect:     false,           // Default: don't wait for ready on collect
		waitReadyTimeout:       5 * time.Second, // Default timeout: 5 seconds
		descs:                  make([]*prometheus.Desc, 0),
	}

	// Apply all options
	for _, opt := range opts {
		opt(b)
	}

	return b
}

// Name returns the collector name
func (b *BaseCollector) Name() string {
	return b.name
}

// RequiresLeaderElection returns whether this collector requires leader election
func (b *BaseCollector) RequiresLeaderElection() bool {
	return b.requiresLeaderElection
}

// SetRequiresLeaderElection sets whether this collector requires leader election
func (b *BaseCollector) SetRequiresLeaderElection(requires bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.requiresLeaderElection = requires
}

// SetLifecycle sets the lifecycle hooks for the collector.
// This allows collectors to add custom start/stop logic without overriding Start()/Stop().
//
// Example:
//
//	c.SetLifecycle(&MyLifecycle{collector: c})
//
// Or using an inline struct:
//
//	c.SetLifecycle(base.LifecycleFuncs{
//	    StartFunc: func(ctx context.Context) error { ... },
//	    StopFunc: func() error { ... },
//	})
func (b *BaseCollector) SetLifecycle(lifecycle Lifecycle) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.lifecycle = lifecycle
}

// Start initializes the collector context
func (b *BaseCollector) Start(ctx context.Context) error {
	b.mu.Lock()

	if b.started {
		b.mu.Unlock()
		return fmt.Errorf("collector %s already started", b.name)
	}

	b.ctx, b.cancel = context.WithCancel(ctx)
	b.started = true
	b.ready = false
	b.readyCh = make(chan struct{})
	b.stoppedCh = make(chan struct{})
	lifecycle := b.lifecycle

	b.mu.Unlock()

	b.logger.WithField("name", b.name).Info("Collector started")

	// Call lifecycle OnStart hook if set (outside the lock to avoid deadlock)
	if lifecycle != nil {
		if err := lifecycle.OnStart(b.ctx); err != nil {
			// OnStart failed, rollback the started state
			b.mu.Lock()

			if b.cancel != nil {
				b.cancel()
				b.cancel = nil
			}

			b.started = false

			b.ready = false
			if b.stoppedCh != nil {
				close(b.stoppedCh)
				b.stoppedCh = nil
			}

			b.mu.Unlock()

			return fmt.Errorf("collector %s OnStart failed: %w", b.name, err)
		}
	}

	return nil
}

// Stop gracefully stops the collector
func (b *BaseCollector) Stop() error {
	b.mu.Lock()

	if !b.started {
		b.mu.Unlock()
		return fmt.Errorf("collector %s not started", b.name)
	}

	lifecycle := b.lifecycle

	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}

	b.started = false
	b.ready = false

	// Close stoppedCh to notify WaitReady callers
	if b.stoppedCh != nil {
		close(b.stoppedCh)
		b.stoppedCh = nil
	}

	b.mu.Unlock()

	// Call lifecycle OnStop hook if set (outside the lock to avoid deadlock)
	if lifecycle != nil {
		if err := lifecycle.OnStop(); err != nil {
			b.logger.WithError(err).
				WithField("name", b.name).
				Warn("Collector OnStop failed, continuing with cleanup")
		}
	}

	b.logger.WithField("name", b.name).Info("Collector stopped")

	return nil
}

// IsStarted returns whether the collector is started
func (b *BaseCollector) IsStarted() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.started
}

// SetReady marks the collector as ready to collect metrics
// Note: Once ready, the collector cannot become not-ready again (except through Stop/Start cycle)
func (b *BaseCollector) SetReady() {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Only set ready once per Start cycle
	if b.ready {
		return
	}

	b.ready = true
	b.logger.WithField("name", b.name).Debug("Collector marked as ready")

	// Close readyCh to notify WaitReady callers
	if b.readyCh != nil {
		close(b.readyCh)
	}
}

// IsReady returns whether the collector is ready to collect metrics
func (b *BaseCollector) IsReady() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ready
}

// WaitReady blocks until the collector is ready to collect metrics
// Returns nil if ready, or an error if context is cancelled or collector is stopped
func (b *BaseCollector) WaitReady(ctx context.Context) error {
	// Get channels and ready status under lock
	b.mu.RLock()
	ready := b.ready
	readyCh := b.readyCh
	stoppedCh := b.stoppedCh
	b.mu.RUnlock()

	// Fast path: already ready
	if ready {
		return nil
	}

	// Check if collector was started
	if readyCh == nil || stoppedCh == nil {
		return fmt.Errorf("collector %s not started", b.name)
	}

	select {
	case <-readyCh:
		return nil
	case <-stoppedCh:
		return fmt.Errorf("collector %s stopped before becoming ready", b.name)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Health performs a basic health check
func (b *BaseCollector) Health() error {
	b.mu.RLock()
	started := b.started
	ctx := b.ctx
	lifecycle := b.lifecycle
	b.mu.RUnlock()

	if !started {
		return fmt.Errorf("collector %s not running", b.name)
	}

	if ctx == nil {
		return fmt.Errorf("collector %s has nil context", b.name)
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("collector %s context cancelled", b.name)
	default:
	}

	// Call lifecycle health check if available
	if lifecycle != nil {
		if err := lifecycle.OnHealth(); err != nil {
			return fmt.Errorf("collector %s health check failed: %w", b.name, err)
		}
	}

	return nil
}

// RegisterDesc registers a prometheus descriptor
func (b *BaseCollector) RegisterDesc(desc *prometheus.Desc) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.descs = append(b.descs, desc)
}

// RunPollLoop executes an immediate poll, marks the collector ready, and then
// polls on the configured interval with consistent duration logging.
func (b *BaseCollector) RunPollLoop(
	ctx context.Context,
	poll PollFunc,
	options PollLoopOptions,
) {
	operation := options.Operation
	if operation == "" {
		operation = "collector"
	}

	interval := options.Interval
	if interval <= 0 {
		interval = time.Minute
	}

	slowThreshold := options.SlowThreshold
	if slowThreshold <= 0 {
		slowThreshold = 500 * time.Millisecond
	}

	b.runPoll(ctx, poll, operation, slowThreshold, options.SuccessInfoLog, true)
	b.SetReady()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.runPoll(ctx, poll, operation, slowThreshold, options.SuccessInfoLog, false)
		case <-ctx.Done():
			b.logger.WithField("operation", operation).Info("Context cancelled, stopping poll loop")
			return
		}
	}
}

func (b *BaseCollector) runPoll(
	ctx context.Context,
	poll PollFunc,
	operation string,
	slowThreshold time.Duration,
	successInfoLog bool,
	initial bool,
) {
	startedAt := time.Now()
	err := poll(ctx)
	duration := time.Since(startedAt)

	fields := log.Fields{
		"name":      b.name,
		"operation": operation,
		"duration":  duration,
		"initial":   initial,
	}

	if err != nil {
		b.logger.WithFields(fields).WithError(err).Error("Collector poll failed")
		return
	}

	if duration > slowThreshold {
		b.logger.WithFields(fields).Warn("Slow collector poll detected")
		return
	}

	if successInfoLog {
		b.logger.WithFields(fields).Info("Collector poll completed")
		return
	}

	b.logger.WithFields(fields).Debug("Collector poll completed")
}

// Describe sends all descriptors to the channel
func (b *BaseCollector) Describe(ch chan<- *prometheus.Desc) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, desc := range b.descs {
		ch <- desc
	}
}

// Collect calls the lifecycle OnCollect hook
func (b *BaseCollector) Collect(ch chan<- prometheus.Metric) {
	b.mu.RLock()
	started := b.started
	ready := b.ready
	lifecycle := b.lifecycle
	b.mu.RUnlock()

	// Only collect metrics if the collector has been started
	if !started {
		return
	}

	// If waitReadyOnCollect is enabled, wait for collector to be ready
	if b.waitReadyOnCollect {
		ctx, cancel := context.WithTimeout(context.Background(), b.waitReadyTimeout)
		defer cancel()

		if err := b.WaitReady(ctx); err != nil {
			b.logger.WithError(err).
				WithField("name", b.name).
				Warn("Collector not ready, skipping metric collection")
			return
		}
	} else if !ready {
		b.logger.WithField("name", b.name).
			Warn("Collector not ready, skipping metric collection")
		return
	}

	if lifecycle != nil {
		lifecycle.OnCollect(ch)
	}
}

// MustRegisterDesc registers a descriptor and panics on error
func (b *BaseCollector) MustRegisterDesc(desc *prometheus.Desc) {
	if desc == nil {
		panic(fmt.Sprintf("collector %s: cannot register nil descriptor", b.name))
	}

	b.RegisterDesc(desc)
}
