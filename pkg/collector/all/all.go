// Package all imports all collector packages to register their factories
package all

import (
	// Import all collectors to trigger their init() functions
	_ "github.com/labring/sealos-state-metrics/pkg/collector/billing"
	_ "github.com/labring/sealos-state-metrics/pkg/collector/cloudbalance"
	_ "github.com/labring/sealos-state-metrics/pkg/collector/cockroachlicense"
	_ "github.com/labring/sealos-state-metrics/pkg/collector/crds"
	_ "github.com/labring/sealos-state-metrics/pkg/collector/database"
	_ "github.com/labring/sealos-state-metrics/pkg/collector/domain"
	_ "github.com/labring/sealos-state-metrics/pkg/collector/imagepull"
	_ "github.com/labring/sealos-state-metrics/pkg/collector/lvm"
	_ "github.com/labring/sealos-state-metrics/pkg/collector/node"
	_ "github.com/labring/sealos-state-metrics/pkg/collector/registryproxy"
	_ "github.com/labring/sealos-state-metrics/pkg/collector/userbalance"
	_ "github.com/labring/sealos-state-metrics/pkg/collector/zombie"
)
