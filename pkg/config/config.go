// Package config provides configuration loading and management for the application
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/alecthomas/kong"
	"github.com/caarlos0/env/v9"
	"github.com/joho/godotenv"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// GlobalConfig contains only top-level application configuration
// Module-specific configs are managed by each module independently
type GlobalConfig struct {
	// Configuration files
	ConfigPath string `yaml:"-" short:"c" help:"Path to configuration file (YAML format)"    type:"path"`
	EnvFile    string `yaml:"-"           help:"Path to .env file for environment variables" type:"path" default:".env"`

	// Server configuration
	Server ServerConfig `yaml:"server" embed:"" prefix:"server-" envprefix:"SERVER_"`

	// Debug server configuration (hot-reloadable, no auth)
	DebugServer DebugServerConfig `yaml:"debugServer" embed:"" prefix:"debug-server-" envprefix:"DEBUG_SERVER_"`

	// Pprof configuration (hot-reloadable)
	Pprof PprofConfig `yaml:"pprof" embed:"" prefix:"pprof-" envprefix:"PPROF_"`

	// Kubernetes client configuration
	Kubernetes KubernetesConfig `yaml:"kubernetes" embed:"" prefix:"" envprefix:"KUBERNETES_"`

	// Metrics configuration
	Metrics MetricsConfig `yaml:"metrics" embed:"" prefix:"metrics-" envprefix:"METRICS_"`

	// Leader election configuration
	LeaderElection LeaderElectionConfig `yaml:"leaderElection" embed:"" prefix:"leader-election-" envprefix:"LEADER_ELECTION_"`

	// Logging configuration
	Logging LoggingConfig `yaml:"logging" embed:"" prefix:"log-" envprefix:"LOGGING_"`

	// Performance tuning
	Performance PerformanceConfig `yaml:"performance" embed:"" prefix:"" envprefix:"PERFORMANCE_"`

	// Enabled collectors (list of collector names)
	EnabledCollectors []string `yaml:"enabledCollectors" help:"Comma-separated list of enabled collectors" default:"domain,node,pod,imagepull,zombie" env:"ENABLED_COLLECTORS" sep:","`

	// Instance identity (optional, defaults to NodeName, POD_NAME, IP, hostname, or random ID)
	Identity string `yaml:"identity" help:"Instance identity (overrides auto-detection)" env:"IDENTITY"`

	// Node name (typically set via downward API for DaemonSet deployments)
	NodeName string `yaml:"nodeName" help:"Node name (for node-level collectors)" env:"NODE_NAME"`

	// Pod name (typically set via downward API)
	PodName string `yaml:"podName" help:"Pod name" env:"POD_NAME"`
}

// ApplyHotReload applies hot-reloadable fields from newConfig
// Note: Server and Logging configs require restart and are not updated
// DebugServer and Pprof configs are hot-reloadable
func (c *GlobalConfig) ApplyHotReload(newConfig *GlobalConfig) {
	c.DebugServer = newConfig.DebugServer
	c.Pprof = newConfig.Pprof
	c.Kubernetes = newConfig.Kubernetes
	c.Metrics = newConfig.Metrics
	c.LeaderElection = newConfig.LeaderElection
	c.Performance = newConfig.Performance
	c.EnabledCollectors = newConfig.EnabledCollectors
	c.Identity = newConfig.Identity
	c.NodeName = newConfig.NodeName
	c.PodName = newConfig.PodName
}

// ServerConfig contains HTTP server configuration
type ServerConfig struct {
	Address     string     `yaml:"address"     name:"address"      env:"ADDRESS"      default:":9090"    help:"Server listen address"`
	MetricsPath string     `yaml:"metricsPath" name:"metrics-path" env:"METRICS_PATH" default:"/metrics" help:"Metrics endpoint path"`
	HealthPath  string     `yaml:"healthPath"  name:"health-path"  env:"HEALTH_PATH"  default:"/health"  help:"Health check endpoint path"`
	TLS         TLSConfig  `yaml:"tls"                                                                                                     embed:"" prefix:"tls-"  envprefix:"TLS_"`
	Auth        AuthConfig `yaml:"auth"                                                                                                    embed:"" prefix:"auth-" envprefix:"AUTH_"`
}

// Equal checks if two ServerConfig are equal
func (c ServerConfig) Equal(other ServerConfig) bool {
	return c.Address == other.Address &&
		c.MetricsPath == other.MetricsPath &&
		c.HealthPath == other.HealthPath &&
		c.TLS.Equal(other.TLS) &&
		c.Auth.Equal(other.Auth)
}

// TLSConfig contains TLS configuration for the HTTP server
type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"  name:"enabled"   env:"ENABLED"   default:"false"            help:"Enable TLS for the metrics server"`
	CertFile string `yaml:"certFile" name:"cert-file" env:"CERT_FILE" default:"/etc/tls/tls.crt" help:"Path to TLS certificate file"      type:"path"`
	KeyFile  string `yaml:"keyFile"  name:"key-file"  env:"KEY_FILE"  default:"/etc/tls/tls.key" help:"Path to TLS private key file"      type:"path"`
}

// Equal checks if two TLSConfig are equal
func (c TLSConfig) Equal(other TLSConfig) bool {
	return c.Enabled == other.Enabled &&
		c.CertFile == other.CertFile &&
		c.KeyFile == other.KeyFile
}

// AuthConfig contains authentication configuration for the metrics endpoint
type AuthConfig struct {
	Enabled bool `yaml:"enabled" name:"enabled" env:"ENABLED" default:"false" help:"Enable Kubernetes authentication for metrics endpoint"`
}

// Equal checks if two AuthConfig are equal
func (c AuthConfig) Equal(other AuthConfig) bool {
	return c.Enabled == other.Enabled
}

// DebugServerConfig contains debug server configuration for internal access without authentication
// This config is hot-reloadable
type DebugServerConfig struct {
	Enabled     bool   `yaml:"enabled"     name:"enabled"      env:"ENABLED"      default:"true"     help:"Enable debug server for internal access (no auth)"`
	Port        int    `yaml:"port"        name:"port"         env:"PORT"         default:"8080"     help:"Debug server port (binds to 127.0.0.1)"`
	MetricsPath string `yaml:"metricsPath" name:"metrics-path" env:"METRICS_PATH" default:"/metrics" help:"Metrics endpoint path for debug server"`
	HealthPath  string `yaml:"healthPath"  name:"health-path"  env:"HEALTH_PATH"  default:"/health"  help:"Health check endpoint path for debug server"`
}

// Equal checks if two DebugServerConfig are equal
func (c DebugServerConfig) Equal(other DebugServerConfig) bool {
	return c.Enabled == other.Enabled &&
		c.Port == other.Port &&
		c.MetricsPath == other.MetricsPath &&
		c.HealthPath == other.HealthPath
}

// PprofConfig contains pprof server configuration for profiling
// This config is hot-reloadable
type PprofConfig struct {
	Enabled bool `yaml:"enabled" name:"enabled" env:"ENABLED" default:"false" help:"Enable pprof server for profiling"`
	Port    int  `yaml:"port"    name:"port"    env:"PORT"    default:"6060"  help:"Pprof server port (binds to 127.0.0.1)"`
}

// Equal checks if two PprofConfig are equal
func (c PprofConfig) Equal(other PprofConfig) bool {
	return c.Enabled == other.Enabled &&
		c.Port == other.Port
}

// KubernetesConfig contains Kubernetes client configuration
type KubernetesConfig struct {
	Kubeconfig string  `yaml:"kubeconfig" name:"kubeconfig" env:"KUBECONFIG" help:"Path to kubeconfig file (leave empty for in-cluster config)" type:"path"`
	QPS        float32 `yaml:"qps"        name:"qps"        env:"QPS"        help:"Kubernetes client QPS limit"                                             envDefault:"50"  default:"50"`
	Burst      int     `yaml:"burst"      name:"burst"      env:"BURST"      help:"Kubernetes client burst limit"                                           envDefault:"100" default:"100"`
}

// Equal checks if two KubernetesConfig are equal
func (c KubernetesConfig) Equal(other KubernetesConfig) bool {
	return c.Kubeconfig == other.Kubeconfig &&
		c.QPS == other.QPS &&
		c.Burst == other.Burst
}

// MetricsConfig contains Prometheus metrics configuration
type MetricsConfig struct {
	Namespace string `yaml:"namespace" name:"namespace" env:"NAMESPACE" help:"Prometheus metrics namespace (optional)"`
}

// LeaderElectionConfig contains leader election configuration
type LeaderElectionConfig struct {
	Enabled       bool          `yaml:"enabled"       name:"enabled"        env:"ENABLED"        envDefault:"true"                default:"true"                help:"Enable leader election"`
	Namespace     string        `yaml:"namespace"     name:"namespace"      env:"NAMESPACE"                                                                     help:"Namespace for leader election lease (empty disables LE)"`
	LeaseName     string        `yaml:"leaseName"     name:"lease-name"     env:"LEASE_NAME"     envDefault:"sealos-state-metric" default:"sealos-state-metric" help:"Name of the leader election lease"`
	LeaseDuration time.Duration `yaml:"leaseDuration" name:"lease-duration" env:"LEASE_DURATION" envDefault:"15s"                 default:"15s"                 help:"Leader election lease duration"`
	RenewDeadline time.Duration `yaml:"renewDeadline" name:"renew-deadline" env:"RENEW_DEADLINE" envDefault:"10s"                 default:"10s"                 help:"Leader election renew deadline"`
	RetryPeriod   time.Duration `yaml:"retryPeriod"   name:"retry-period"   env:"RETRY_PERIOD"   envDefault:"2s"                  default:"2s"                  help:"Leader election retry period"`
}

// LoggingConfig contains logging configuration
type LoggingConfig struct {
	Level  string `yaml:"level"  name:"level"  env:"LEVEL"  default:"info"  enum:"debug,info,warn,error" help:"Log level"`
	Format string `yaml:"format" name:"format" env:"FORMAT" default:"text"  enum:"json,text"             help:"Log format"`
	Debug  bool   `yaml:"debug"  name:"debug"  env:"DEBUG"  default:"false"                              help:"Enable debug mode"`
}

// ToLoggerOptions converts LoggingConfig to logger initialization options
func (c *LoggingConfig) ToLoggerOptions() (debug bool, level, format string) {
	return c.Debug, c.Level, c.Format
}

// PerformanceConfig contains performance tuning configuration
type PerformanceConfig struct {
	InformerResyncPeriod time.Duration `yaml:"informerResyncPeriod" name:"informer-resync-period" env:"INFORMER_RESYNC_PERIOD" envDefault:"10m" default:"10m" help:"Kubernetes informer resync period" hidden:""`
}

// LoadEnvFile loads environment variables from a .env file
func LoadEnvFile(path string) error {
	return godotenv.Load(path)
}

// LoadFromYAML loads configuration from YAML file into cfg
func LoadFromYAML(path string, cfg *GlobalConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	return LoadFromYAMLContent(data, cfg)
}

// LoadFromYAMLContent loads configuration from YAML content into cfg
func LoadFromYAMLContent(content []byte, cfg *GlobalConfig) error {
	if err := yaml.Unmarshal(content, cfg); err != nil {
		return fmt.Errorf("failed to unmarshal YAML: %w", err)
	}

	return nil
}

// ParseEnv parses environment variables into cfg
func ParseEnv(cfg *GlobalConfig) error {
	return env.Parse(cfg)
}

// ConfigLoader loads configuration from multiple sources with proper precedence
type ConfigLoader struct {
	configPath string
	envFile    string
}

// NewConfigLoader creates a new configuration loader
func NewConfigLoader(configPath, envFile string) *ConfigLoader {
	return &ConfigLoader{
		configPath: configPath,
		envFile:    envFile,
	}
}

// Load loads configuration with precedence: CLI flags > ENV vars > Config file > Defaults
func (l *ConfigLoader) Load() (*GlobalConfig, error) {
	// Step 1: Load .env file if specified (lowest priority, before system env)
	if l.envFile != "" {
		if err := godotenv.Load(l.envFile); err != nil {
			log.WithFields(log.Fields{
				"file":  l.envFile,
				"error": err,
			}).Debug("No .env file loaded")
		} else {
			log.WithField("file", l.envFile).Info("Loaded environment from .env file")
		}
	}

	// Step 2: Start with defaults (built into struct tags via envDefault)
	cfg := &GlobalConfig{
		EnabledCollectors: []string{"domain", "node", "pod", "imagepull", "zombie"},
	}

	// Step 3: Load from YAML config file if specified
	if l.configPath != "" {
		if err := l.loadFromYAML(cfg); err != nil {
			return nil, fmt.Errorf("failed to load config from YAML: %w", err)
		}
	}

	// Step 4: Override with environment variables (highest priority)
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse environment variables: %w", err)
	}

	log.WithFields(log.Fields{
		"server":         cfg.Server.Address,
		"collectors":     cfg.EnabledCollectors,
		"leaderElection": cfg.LeaderElection.Enabled,
	}).Info("Configuration loaded")

	return cfg, nil
}

// loadFromYAML loads configuration from YAML file
func (l *ConfigLoader) loadFromYAML(cfg *GlobalConfig) error {
	data, err := os.ReadFile(l.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("failed to unmarshal YAML: %w", err)
	}

	log.WithField("file", l.configPath).Info("Configuration loaded from YAML")

	return nil
}

// Validate validates the global configuration
func (c *GlobalConfig) Validate() error {
	if c.Server.Address == "" {
		return errors.New("server.address cannot be empty")
	}

	// Auto-disable leader election if namespace is empty
	if c.LeaderElection.Namespace == "" {
		if c.LeaderElection.Enabled {
			log.Warn("Leader election namespace is empty, automatically disabling leader election")

			c.LeaderElection.Enabled = false
		}
	} else if c.LeaderElection.Enabled {
		// Only validate timing constraints if leader election is enabled
		if c.LeaderElection.RenewDeadline >= c.LeaderElection.LeaseDuration {
			return errors.New("leaderElection.renewDeadline must be less than leaseDuration")
		}
	}

	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLevels[c.Logging.Level] {
		return fmt.Errorf("invalid logging.level: %s", c.Logging.Level)
	}

	return nil
}

// LoadOptions contains options for loading configuration
type LoadOptions struct {
	// Args are CLI arguments (without program name)
	Args []string
	// ConfigContent is YAML config content (if provided, takes precedence over file)
	ConfigContent []byte
	// EnvFile is path to .env file
	EnvFile string
	// DisableExit disables automatic exit on --help or parse errors (for config reload)
	DisableExit bool
}

// LoadGlobalConfig loads configuration with priority: CLI flags (defaults) < YAML < env vars
// This function can be used for both initial load and reload
func LoadGlobalConfig(opts LoadOptions) (*GlobalConfig, error) {
	cfg := &GlobalConfig{}

	// Step 1: Parse CLI args with kong (applies defaults)
	kongOpts := []kong.Option{
		kong.Name("sealos-state-metric"),
		kong.Description("Sealos state metrics collector for Kubernetes"),
	}

	// Only disable exit during config reload
	if opts.DisableExit {
		kongOpts = append(kongOpts, kong.Exit(func(int) {}))
	}

	parser, err := kong.New(cfg, kongOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create config parser: %w", err)
	}

	if _, err := parser.Parse(opts.Args); err != nil {
		return nil, fmt.Errorf("failed to parse CLI args: %w", err)
	}

	// Step 2: Load .env file if specified
	if opts.EnvFile != "" {
		if err := godotenv.Load(opts.EnvFile); err != nil {
			log.WithFields(log.Fields{
				"file":  opts.EnvFile,
				"error": err,
			}).Debug("No .env file loaded")
		} else {
			log.WithField("file", opts.EnvFile).Info("Loaded environment from .env file")
		}
	}

	// Step 3: Overlay YAML config
	if len(opts.ConfigContent) > 0 {
		if err := yaml.Unmarshal(opts.ConfigContent, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse YAML config: %w", err)
		}
	} else if cfg.ConfigPath != "" {
		if err := LoadFromYAML(cfg.ConfigPath, cfg); err != nil {
			return nil, fmt.Errorf("failed to load config from YAML: %w", err)
		}
	}

	// Step 4: Overlay environment variables (highest priority)
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse environment variables: %w", err)
	}

	return cfg, nil
}
