// Package database provides database connection and pool management.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConfig contains PostgreSQL connection pool configuration.
type PoolConfig struct {
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
	PingTimeout       time.Duration
}

// DefaultPoolConfig returns default pool configuration.
func DefaultPoolConfig() *PoolConfig {
	return &PoolConfig{
		MaxConns:          25,
		MinConns:          5,
		MaxConnLifetime:   time.Hour,
		MaxConnIdleTime:   30 * time.Minute,
		HealthCheckPeriod: time.Minute,
		PingTimeout:       5 * time.Second,
	}
}

// PoolOption is a functional option for configuring PoolConfig.
type PoolOption func(*PoolConfig)

// WithMaxConns sets the maximum number of connections in the pool.
func WithMaxConns(n int32) PoolOption {
	return func(c *PoolConfig) {
		c.MaxConns = n
	}
}

// WithMinConns sets the minimum number of connections in the pool.
func WithMinConns(n int32) PoolOption {
	return func(c *PoolConfig) {
		c.MinConns = n
	}
}

// WithMaxConnLifetime sets the maximum lifetime of connections in the pool.
func WithMaxConnLifetime(d time.Duration) PoolOption {
	return func(c *PoolConfig) {
		c.MaxConnLifetime = d
	}
}

// WithMaxConnIdleTime sets the maximum idle time for connections in the pool.
func WithMaxConnIdleTime(d time.Duration) PoolOption {
	return func(c *PoolConfig) {
		c.MaxConnIdleTime = d
	}
}

// WithHealthCheckPeriod sets the period between health checks.
func WithHealthCheckPeriod(d time.Duration) PoolOption {
	return func(c *PoolConfig) {
		c.HealthCheckPeriod = d
	}
}

// WithPingTimeout sets the timeout for database ping operations.
func WithPingTimeout(d time.Duration) PoolOption {
	return func(c *PoolConfig) {
		c.PingTimeout = d
	}
}

// InitPgClient initializes a PostgreSQL connection pool with the given DSN and options.
func InitPgClient(ctx context.Context, dsn string, opts ...PoolOption) (*pgxpool.Pool, error) {
	poolConfig := DefaultPoolConfig()

	for _, opt := range opts {
		opt(poolConfig)
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse DSN: %w", err)
	}

	config.MaxConns = poolConfig.MaxConns
	config.MinConns = poolConfig.MinConns
	config.MaxConnLifetime = poolConfig.MaxConnLifetime
	config.MaxConnIdleTime = poolConfig.MaxConnIdleTime
	config.HealthCheckPeriod = poolConfig.HealthCheckPeriod

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, poolConfig.PingTimeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return pool, nil
}
