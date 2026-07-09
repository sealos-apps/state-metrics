package billing

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	resourceCPU       = "0"
	resourceMemory    = "1"
	resourceStorage   = "2"
	resourceNetwork   = "3"
	resourceNodePorts = "4"
)

const (
	billingTypeConsumption = 0
)

const (
	appTypeDB            = 1
	appTypeAPP           = 2
	appTypeObjectStorage = 6
)

const (
	billingStatusSettled      = 1
	billingStatusSubscription = 2
)

var defaultProperties = map[string]PropertyInfo{
	resourceCPU:       {Name: "cpu", Unit: "1m"},
	resourceMemory:    {Name: "memory", Unit: "1Mi"},
	resourceStorage:   {Name: "storage", Unit: "1Mi"},
	resourceNetwork:   {Name: "network", Unit: "1Mi"},
	resourceNodePorts: {Name: "services.nodeports", Unit: "1"},
}

// PropertyInfo maps a Sealos billing property enum to a stable Prometheus label.
type PropertyInfo struct {
	Name string
	Unit string
}

type resourceKey struct {
	Resource  string
	Unit      string
	Owner     string
	Namespace string
}

type resourceTotal struct {
	Used float64
}

type billingAggregateRow struct {
	Owner     string `bson:"owner"`
	Namespace string `bson:"namespace"`
	Resource  string `bson:"resource"`
	Used      any    `bson:"used"`
}

type BillingSnapshot struct {
	StartedAt   time.Time
	FinishedAt  time.Time
	WindowStart time.Time
	WindowEnd   time.Time

	Resources      map[resourceKey]resourceTotal
	OwnerResources map[resourceKey]resourceTotal
	Metrics        []prometheus.Metric
}
