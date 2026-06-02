package registryproxy

import "time"

// RegistryConfig holds configuration for a single registry proxy.
type RegistryConfig struct {
	Endpoint             string            `yaml:"endpoint"`
	Info                 string            `yaml:"info"`
	IPs                  []string          `yaml:"ips"`
	Repository           string            `yaml:"repository"`
	Reference            string            `yaml:"reference"`
	ManifestAcceptHeader string            `yaml:"manifestAcceptHeader"`
	Headers              map[string]string `yaml:"headers"`
}

// Config contains configuration for the RegistryProxy collector.
type Config struct {
	Registries    []RegistryConfig `yaml:"registries"    env:"-"`
	CheckTimeout  time.Duration    `yaml:"checkTimeout"  env:"CHECK_TIMEOUT"`
	CheckInterval time.Duration    `yaml:"checkInterval" env:"CHECK_INTERVAL"`
}

// NewDefaultConfig returns the default configuration for RegistryProxy collector.
func NewDefaultConfig() *Config {
	return &Config{
		Registries:    []RegistryConfig{},
		CheckTimeout:  30 * time.Second,
		CheckInterval: 1 * time.Minute,
	}
}
