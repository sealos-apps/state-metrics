package userbalance

import (
	"time"
)

// DatabaseConfig holds database connection configuration
type DatabaseConfig struct {
	// DSN connects to the global user and account database.
	DSN string `yaml:"dsn" json:"dsn"`
	// LocalDSN optionally connects to the regional UserCr database.
	LocalDSN string `yaml:"localDsn" json:"local_dsn" env:"LOCAL_DSN"`
}

// UserConfig holds configuration for a single user/account
type UserConfig struct {
	Region string  `yaml:"region" json:"region"` // Cloud service region
	UUID   string  `yaml:"uuid"   json:"uuid"`   // User unique identifier
	UID    string  `yaml:"uid"    json:"uid"`    // User ID
	Owner  string  `yaml:"owner"  json:"owner"`  // Account owner
	Type   string  `yaml:"type"   json:"type"`   // User type
	Level  string  `yaml:"level"  json:"level"`  // User level
	Quota  float64 `yaml:"quota"  json:"quota"`  // User Quota
}

// Config contains configuration for the UserBalance collector
type Config struct {
	// DatabaseConfig stores the PostgreSQL/CockroachDB connection string.
	DatabaseConfig DatabaseConfig `yaml:"database" json:"database"`
	// UserConfig lists explicitly monitored users.
	UserConfig []UserConfig `yaml:"users" json:"users"`
	// CheckInterval controls balance polling frequency.
	CheckInterval time.Duration `yaml:"checkInterval" json:"check_interval" env:"CHECK_INTERVAL"`
	// PositiveBalanceUsers discovers users with positive available balance.
	PositiveBalanceUsers bool `yaml:"positiveBalanceUsers" json:"positive_balance_users" env:"POSITIVE_BALANCE_USERS"`
}

// NewDefaultConfig returns the default configuration for UserBalance collector
func NewDefaultConfig() *Config {
	return &Config{
		DatabaseConfig: DatabaseConfig{
			DSN:      "",
			LocalDSN: "",
		},
		UserConfig:    []UserConfig{},
		CheckInterval: 5 * time.Minute,
	}
}
