package billing

import "time"

const (
	defaultAccountDB            = "sealos-resources"
	defaultBillingCollection    = "billing"
	defaultPropertiesCollection = "properties"
)

// MongoConfig holds MongoDB connection information for Sealos billing data.
type MongoConfig struct {
	URI                  string `yaml:"uri"                  json:"uri"                   env:"URI"`
	Database             string `yaml:"database"             json:"database"              env:"DATABASE"`
	BillingCollection    string `yaml:"billingCollection"    json:"billing_collection"    env:"BILLING_COLLECTION"`
	PropertiesCollection string `yaml:"propertiesCollection" json:"properties_collection" env:"PROPERTIES_COLLECTION"`
}

// Config contains configuration for the Sealos billing collector.
type Config struct {
	Mongo MongoConfig `yaml:"mongo" json:"mongo" envPrefix:"MONGO_"`

	ScrapeInterval     time.Duration `yaml:"scrapeInterval"     json:"scrape_interval"      env:"SCRAPE_INTERVAL"`
	QueryTimeout       time.Duration `yaml:"queryTimeout"       json:"query_timeout"        env:"QUERY_TIMEOUT"`
	EnableOwnerMetrics bool          `yaml:"enableOwnerMetrics" json:"enable_owner_metrics" env:"ENABLE_OWNER_METRICS"`
}

// NewDefaultConfig returns the default configuration for the billing collector.
func NewDefaultConfig() *Config {
	return &Config{
		Mongo: MongoConfig{
			Database:             defaultAccountDB,
			BillingCollection:    defaultBillingCollection,
			PropertiesCollection: defaultPropertiesCollection,
		},
		ScrapeInterval: 5 * time.Minute,
		QueryTimeout:   30 * time.Second,
	}
}
