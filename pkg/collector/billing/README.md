# Billing Collector

The billing collector reads finalized Sealos consumption billing records from
MongoDB and exposes Prometheus metrics for billed resource usage.

The primary metric for capacity accounting is `*_resource_usage`. Sealos
consumption billing records are already generated from an hourly billing
window, so `app_costs[].used` is the hourly billed usage.

## Sealos Billing Storage

Sealos writes billing-related data to MongoDB through two stages.

1. The resources controller samples live resource usage and writes raw samples
   to daily time-series collections named `monitor_YYYYMMDD`.
2. The account controller runs hourly, reads the previous full-hour monitor
   samples, and writes finalized consumption records to the `billing`
   collection.

Default MongoDB locations:

| Purpose | Default |
| --- | --- |
| Database | `sealos-resources` |
| Billing collection | `billing` |
| Property collection | `properties` |
| Raw monitor collection pattern | `monitor_YYYYMMDD` |

The collector reads the finalized `billing` collection and uses `properties` to
resolve resource enum ids into resource names and billing units.

Currency and price data stay outside this collector because every cluster can
configure resource prices independently through Sealos properties.

## Raw MongoDB Examples

## Raw Data Characteristics

The raw Sealos data has several important characteristics:

| Area | Characteristic |
| --- | --- |
| Write path | `monitor_YYYYMMDD` is written by the resources controller; `billing` is written by the account controller. |
| Billing cadence | `billing` records are produced hourly from the previous complete hour of monitor data. |
| Billing timestamp | `billing.time` is the billing window end time. A record with `time=10:00` represents usage from `09:00` to `10:00`. |
| Monitor timestamp | `monitor_YYYYMMDD.time` is the sample time of a raw resource observation. |
| Monitor collection sharding | Raw monitor collections are split by UTC day using the `monitor_YYYYMMDD` name pattern. |
| Resource key format | Resource usage maps use enum ids as BSON object keys, normally stored as strings like `"0"`, `"1"`, `"2"`. |
| Resource metadata | `properties.enum` maps those keys to resource names, units, and price configuration. |
| CPU unit | CPU usage uses `1m`; divide by `1000` to convert to CPU cores. |
| Memory and storage unit | Memory and storage normally use `1Mi`. |
| Network unit | Network normally uses `1Mi`; Sealos stores hourly sent traffic for billing. |
| Price fields | `billing.amount` is the record total. `app_costs[].amount` is the app item total. `app_costs[].used_amount` is the resource-level amount map. Sealos stores raw amount values in 1/1000000 base units. The collector emits raw amount values without currency labels. |
| Aggregation type | The `properties.price_type` controls how billing was generated: `AVG`, `SUM`, or `DIF`. The finalized `billing.app_costs[].used` already reflects that aggregation. |
| Ownership | `owner` is the Sealos user owner. `namespace` is the charged namespace. |
| App grouping | `app_type` identifies broad Sealos app categories; `app_costs[].type` and `app_costs[].name` identify entries inside the billing record. |
| Settlement status | `status=1` means settled, `status=0` means unsettled, and `status=2` means subscription. |
| Deduplication | Sealos creates a unique index on `(owner, order_id)`. The collector sums records returned by the time/status/type filter. |
| Delay tolerance | The collector queries the previous complete hourly window, so it avoids partially generated current-hour records. |
| Missing metadata | If a resource enum is absent from `properties`, the collector emits `resource_unknown_<enum>` with unit `1`. |

The collector treats `billing` as the source of truth. It reads the finalized
hourly usage and does not recompute billing from `monitor_YYYYMMDD`.

### `properties`

The `properties` collection maps resource enum ids to names and units. A real
cluster can add more resources or override prices.

```json
{
  "_id": { "$oid": "66f000000000000000000001" },
  "name": "cpu",
  "alias": "cpu",
  "enum": 0,
  "price_type": "AVG",
  "unit_price": 2.237442922,
  "view_price": 0,
  "encrypt_unit_price": "",
  "unit": "1m",
  "unit_period": ""
}
```

```json
{
  "_id": { "$oid": "66f000000000000000000002" },
  "name": "memory",
  "alias": "memory",
  "enum": 1,
  "price_type": "AVG",
  "unit_price": 1.092501427,
  "unit": "1Mi"
}
```

### `monitor_YYYYMMDD`

Raw resource samples are written before billing aggregation. The collector uses
the finalized `billing` records, while this example explains where the hourly
values come from.

```json
{
  "_id": { "$oid": "66f100000000000000000001" },
  "time": { "$date": "2026-07-08T09:12:00Z" },
  "category": "ns-user-a",
  "type": 2,
  "parent_type": 0,
  "parent_name": "",
  "name": "launchpad-api",
  "used": {
    "0": 500,
    "1": 1024
  }
}
```

For this monitor sample:

| Field | Meaning |
| --- | --- |
| `category` | Namespace. |
| `type` | App type enum. `2` means `APP`. |
| `name` | App/resource name inside the namespace. |
| `used.0` | CPU in `1m` units, so `500` means `500m`. |
| `used.1` | Memory in `1Mi` units, so `1024` means `1024Mi`. |

### `billing`

The account controller converts raw monitor samples from the billing hour into
finalized consumption records. This collector reads records with `type = 0`.

```json
{
  "_id": { "$oid": "66f200000000000000000001" },
  "time": { "$date": "2026-07-08T10:00:00Z" },
  "order_id": "nanoid123456",
  "type": 0,
  "namespace": "ns-user-a",
  "app_type": 2,
  "app_name": "",
  "owner": "user-a",
  "status": 1,
  "amount": 123456,
  "app_costs": [
    {
      "type": 2,
      "name": "launchpad-api",
      "used": {
        "0": 500,
        "1": 1024,
        "3": 2048
      },
      "used_amount": {
        "0": 1119,
        "1": 1119,
        "3": 0
      },
      "amount": 2238
    }
  ]
}
```

For this billing record:

| Field | Meaning |
| --- | --- |
| `time` | Billing window end time. `2026-07-08T10:00:00Z` represents the `09:00-10:00` billing hour. |
| `type` | Billing record type. `0` means consumption. |
| `namespace` | Charged namespace. |
| `owner` | Charged Sealos owner. |
| `app_type` | App type enum. `2` means `APP`. |
| `status` | Billing status enum. `1` means `settled`. |
| `app_costs[].used` | Hourly billed resource usage. |
| `app_costs[].used_amount` | Price-derived amount by resource enum. The collector reads this map for resource amount metrics. |

`app_costs[].used` in the example maps to:

| Key | Resource | Unit | Value meaning |
| --- | --- | --- | --- |
| `0` | `cpu` | `1m` | `500m`, equal to `0.5` average CPU cores for the billing hour. |
| `1` | `memory` | `1Mi` | `1024Mi` average memory for the billing hour. |
| `3` | `network` | `1Mi` | `2048Mi` sent during the billing hour. |

## Hourly Query Window

The collector always reads the previous complete hourly billing window.

At `10:35`, the collector computes:

```text
windowEnd   = 2026-07-08T10:00:00Z
windowStart = 2026-07-08T09:00:00Z
```

MongoDB filter:

```json
{
  "type": 0,
  "time": {
    "$gt": { "$date": "2026-07-08T09:00:00Z" },
    "$lte": { "$date": "2026-07-08T10:00:00Z" }
  }
}
```

The `>` and `<=` bounds match Sealos billing records whose `time` is the end of
the hourly billing window.

## Hourly Usage Semantics

For default Sealos resources:

| Resource | Unit | Billing meaning |
| --- | --- | --- |
| `cpu` | `1m` | Average mCPU used during the billing hour. Divide by `1000` to get average cores. |
| `memory` | `1Mi` | Average MiB used during the billing hour. |
| `storage` | `1Mi` | Average MiB used during the billing hour. |
| `network` | `1Mi` | Total MiB sent during the billing hour. |
| `services.nodeports` | `1` | Average billed count during the billing hour. |

For example, this metric means the selected billing records used `2500m` CPU
from Unix second `1760000000` to `1760003600`:

```text
sealos_billing_resource_usage{window_start="1760000000",window_end="1760003600",resource="cpu",unit="1m"} 2500
```

Equivalent average cores:

```promql
sum(sealos_billing_resource_usage{resource="cpu", unit="1m"}) / 1000
```

## Resource Metadata

Default fallback mapping:

| Enum | Resource | Raw unit |
| --- | --- | --- |
| `0` | `cpu` | `1m` |
| `1` | `memory` | `1Mi` |
| `2` | `storage` | `1Mi` |
| `3` | `network` | `1Mi` |
| `4` | `services.nodeports` | `1` |

Additional enums from `properties` are collected automatically. Unknown enums
are emitted as `resource_unknown_<enum>` with raw unit `1`.

## Metric Prefix

Metric names use the configured Prometheus namespace:

```yaml
metrics:
  namespace: "sealos"
```

The examples below use the default `sealos` prefix. With another namespace,
replace the `sealos_` prefix accordingly.

## Metrics

The collector emits these metric names:

| Metric | Meaning |
| --- | --- |
| `sealos_billing_resource_usage` | Cluster-wide hourly billed usage by resource and unit. |
| `sealos_billing_owner_resource_usage` | Hourly billed usage by owner, namespace, resource, and unit. Emitted only when `enableOwnerMetrics=true`. |
| `sealos_billing_resource_amount` | Cluster-wide billing amount by resource over the previous complete hourly window. |
| `sealos_billing_owner_resource_amount` | Billing amount by owner, namespace, and resource over the previous complete hourly window. Emitted only when `enableOwnerMetrics=true`. |
| `sealos_billing_last_success_timestamp_seconds` | Last successful MongoDB collection Unix timestamp. |

`window_start` and `window_end` are Unix seconds and identify the exact billing
hour represented by each usage series.

### `sealos_billing_resource_usage`

Raw hourly billed resource usage aggregated across all owners and namespaces in
the previous complete hourly billing window.

Type: Gauge

Labels:

| Label | Source |
| --- | --- |
| `window_start` | Billing query window start Unix seconds. |
| `window_end` | Billing query window end Unix seconds. |
| `resource` | `properties.name`, fallback defaults, or `resource_unknown_<enum>` |
| `unit` | `properties.unit`, fallback defaults, or `1` |

Examples:

```text
sealos_billing_resource_usage{window_start="1760000000",window_end="1760003600",resource="cpu",unit="1m"} 2500
sealos_billing_resource_usage{window_start="1760000000",window_end="1760003600",resource="memory",unit="1Mi"} 4096
sealos_billing_resource_usage{window_start="1760000000",window_end="1760003600",resource="storage",unit="1Mi"} 102400
sealos_billing_resource_usage{window_start="1760000000",window_end="1760003600",resource="network",unit="1Mi"} 20480
sealos_billing_resource_usage{window_start="1760000000",window_end="1760003600",resource="services.nodeports",unit="1"} 3
```

### `sealos_billing_owner_resource_usage`

Raw hourly billed resource usage grouped by Sealos owner and namespace.

Type: Gauge

Labels:

| Label | Source |
| --- | --- |
| `window_start` | Billing query window start Unix seconds. |
| `window_end` | Billing query window end Unix seconds. |
| `owner` | `billing.owner` |
| `namespace` | `billing.namespace` |
| `resource` | `properties.name`, fallback defaults, or `resource_unknown_<enum>` |
| `unit` | raw unit from properties |

Examples:

```text
sealos_billing_owner_resource_usage{window_start="1760000000",window_end="1760003600",owner="user-a",namespace="ns-user-a",resource="cpu",unit="1m"} 1250
sealos_billing_owner_resource_usage{window_start="1760000000",window_end="1760003600",owner="user-a",namespace="ns-user-a",resource="memory",unit="1Mi"} 2048
sealos_billing_owner_resource_usage{window_start="1760000000",window_end="1760003600",owner="user-a",namespace="ns-user-a",resource="storage",unit="1Mi"} 51200
sealos_billing_owner_resource_usage{window_start="1760000000",window_end="1760003600",owner="user-a",namespace="ns-user-a",resource="network",unit="1Mi"} 10240
```

### `sealos_billing_resource_amount`

Raw billing amount grouped by resource across all owners and namespaces. The
value comes from `billing.app_costs[].used_amount`.

Type: Gauge

Labels:

| Label | Source |
| --- | --- |
| `window_start` | Billing query window start Unix seconds. |
| `window_end` | Billing query window end Unix seconds. |
| `resource` | `properties.name`, fallback defaults, or `resource_unknown_<enum>` |

Example:

```text
sealos_billing_resource_amount{window_start="1760000000",window_end="1760003600",resource="cpu"} 67124
sealos_billing_resource_amount{window_start="1760000000",window_end="1760003600",resource="memory"} 33512
sealos_billing_resource_amount{window_start="1760000000",window_end="1760003600",resource="services.nodeports"} 2083
```

### `sealos_billing_owner_resource_amount`

Raw billing amount grouped by Sealos owner, namespace, and resource. This metric
is emitted when `enableOwnerMetrics=true`.

Type: Gauge

Labels:

| Label | Source |
| --- | --- |
| `window_start` | Billing query window start Unix seconds. |
| `window_end` | Billing query window end Unix seconds. |
| `owner` | `billing.owner` |
| `namespace` | `billing.namespace` |
| `resource` | `properties.name`, fallback defaults, or `resource_unknown_<enum>` |

Example:

```text
sealos_billing_owner_resource_amount{window_start="1760000000",window_end="1760003600",owner="user-a",namespace="ns-user-a",resource="cpu"} 33562
sealos_billing_owner_resource_amount{window_start="1760000000",window_end="1760003600",owner="user-a",namespace="ns-user-a",resource="memory"} 16756
```

### `sealos_billing_last_success_timestamp_seconds`

Unix timestamp of the last successful MongoDB collection.

Type: Gauge

Labels: none

Example:

```text
sealos_billing_last_success_timestamp_seconds 1760003615
```

## Raw Field Reference

### `app_type`

Billing records contain `app_type`, but the collector merges data across app
types in metrics. Known Sealos app type values:

| Enum | Label |
| --- | --- |
| `1` | `DB` |
| `2` | `APP` |
| `3` | `TERMINAL` |
| `4` | `JOB` |
| `5` | `OTHER` |
| `6` | `OBJECT-STORAGE` |
| `7` | `CLOUD-VM` |
| `8` | `APP-STORE` |
| `9` | `DB-BACKUP` |
| `10` | `DEV-BOX` |
| `11` | `LLM-TOKEN` |

### `status`

Billing records contain `status`, but the collector merges data across statuses
in metrics. Known Sealos billing status values:

| Enum | Label |
| --- | --- |
| `0` | `unsettled` |
| `1` | `settled` |
| `2` | `subscription` |

## Configuration

```yaml
enabledCollectors:
  - billing

collectors:
  billing:
    scrapeInterval: "5m"
    queryTimeout: "30s"
    enableOwnerMetrics: false
    mongo:
      uri: "mongodb://username:password@mongodb.account-system.svc:27017"
      database: "sealos-resources"
      billingCollection: "billing"
      propertiesCollection: "properties"
```

Fields:

| Field | Default | Meaning |
| --- | --- | --- |
| `scrapeInterval` | `5m` | MongoDB scrape interval. |
| `queryTimeout` | `30s` | MongoDB connection and query timeout. |
| `enableOwnerMetrics` | `false` | Enable owner/namespace-level Mongo aggregation and `sealos_billing_owner_*` metrics. |
| `mongo.uri` | empty | MongoDB URI. |
| `mongo.database` | `sealos-resources` | MongoDB database name. |
| `mongo.billingCollection` | `billing` | MongoDB billing collection name. |
| `mongo.propertiesCollection` | `properties` | MongoDB properties collection name. |

Environment variable prefix:

```text
COLLECTORS_BILLING_
```

Examples:

```bash
COLLECTORS_BILLING_MONGO_URI='mongodb://user:pass@mongo:27017'
COLLECTORS_BILLING_SCRAPE_INTERVAL='5m'
COLLECTORS_BILLING_QUERY_TIMEOUT='30s'
COLLECTORS_BILLING_ENABLE_OWNER_METRICS='false'
```

## Mongo Query Behavior

Each scrape reads the previous complete UTC hour:

```text
windowEnd   = now.UTC().Truncate(time.Hour)
windowStart = windowEnd - 1h
```

The collector queries `billing` with this match condition:

```javascript
{
  type: 0,
  time: {
    $gt: ISODate("<windowStart>"),
    $lte: ISODate("<windowEnd>")
  }
}
```

Aggregation is executed inside MongoDB with two independent aggregation
commands: one for `app_costs.used`, and one for `app_costs.used_amount`. The two
commands run concurrently during a scrape and each command returns one cursor row
per aggregate group.

With `enableOwnerMetrics=false`, MongoDB groups resources and resource amounts
by resource enum. The collector emits cluster-level samples.

With `enableOwnerMetrics=true`, MongoDB additionally groups by `owner` and
`namespace`, and the collector emits the owner-level metrics.

1. Match the previous complete billing hour.
2. In the resource usage aggregation, project `owner`, `namespace`, and
   `app_costs`.
3. Unwind `app_costs`.
4. Convert `app_costs.used` from an object such as `{ "0": 500, "1": 1024 }`
   into key/value rows.
5. Group by `owner`, `namespace`, and resource enum.
6. Sum `used` per resource group.
7. In the resource amount aggregation, convert `app_costs.used_amount` into
   key/value rows.
8. Sum `used_amount` per resource group.

The collector uses separate cursor results for usage and amount aggregates. This
keeps high-cardinality owner results distributed across MongoDB cursor batches.

The collector receives rows shaped like this:

```json
{
  "owner": "user-a",
  "namespace": "ns-user-a",
  "resource": "0",
  "used": 2500
}
```

The resource enum is then mapped through `properties` and emitted as
Prometheus labels.

Resource amount rows have the same enum mapping:

```json
{
  "owner": "user-a",
  "namespace": "ns-user-a",
  "resource": "0",
  "amount": 67124
}
```

### Recommended Index

For large billing collections, create an index matching the collector's hourly
query:

```javascript
db.getSiblingDB("sealos-resources").billing.createIndex(
  { type: 1, time: 1 },
  { name: "sealos_state_metrics_type_time" }
)
```

The Sealos account controller creates an owner-oriented billing index:

```javascript
{ owner: 1, time: 1, type: 1 }
```

That index is useful for owner-scoped account queries. The collector scans by
`type` and `time`, so `{ type: 1, time: 1 }` gives MongoDB a direct range scan
for the scrape window.

Check the resource usage query plan with:

```javascript
db.getSiblingDB("sealos-resources").billing.explain("executionStats").aggregate([
  {
    $match: {
      type: 0,
      time: {
        $gt: ISODate("2026-07-09T01:00:00Z"),
        $lte: ISODate("2026-07-09T02:00:00Z")
      }
    }
  },
  { $project: { owner: 1, namespace: 1, app_costs: 1 } },
  { $unwind: "$app_costs" },
  {
    $project: {
      owner: 1,
      namespace: 1,
      used: { $objectToArray: { $ifNull: ["$app_costs.used", {}] } }
    }
  },
  { $unwind: "$used" },
  {
    $group: {
      _id: {
        owner: "$owner",
        namespace: "$namespace",
        resource: "$used.k"
      },
      used: { $sum: "$used.v" }
    }
  },
  {
    $project: {
      _id: 0,
      owner: "$_id.owner",
      namespace: "$_id.namespace",
      resource: "$_id.resource",
      used: 1
    }
  }
])
```

Check the resource amount query plan with:

```javascript
db.getSiblingDB("sealos-resources").billing.explain("executionStats").aggregate([
  {
    $match: {
      type: 0,
      time: {
        $gt: ISODate("2026-07-09T01:00:00Z"),
        $lte: ISODate("2026-07-09T02:00:00Z")
      }
    }
  },
  { $project: { owner: 1, namespace: 1, app_costs: 1 } },
  { $unwind: "$app_costs" },
  {
    $project: {
      owner: 1,
      namespace: 1,
      amount: { $objectToArray: { $ifNull: ["$app_costs.used_amount", {}] } }
    }
  },
  { $unwind: "$amount" },
  {
    $group: {
      _id: {
        owner: "$owner",
        namespace: "$namespace",
        resource: "$amount.k"
      },
      amount: { $sum: "$amount.v" }
    }
  },
  {
    $project: {
      _id: 0,
      owner: "$_id.owner",
      namespace: "$_id.namespace",
      resource: "$_id.resource",
      amount: 1
    }
  }
])
```

Total billing amount for the previous complete hourly billing window:

```promql
sum(sealos_billing_resource_amount) / 1000000
```

Billing amount by resource:

```promql
sum by (resource) (sealos_billing_resource_amount) / 1000000
```

Billing amount by owner and resource. Requires `enableOwnerMetrics=true`:

```promql
sum by (owner, resource) (sealos_billing_owner_resource_amount) / 1000000
```

## PromQL Examples

Total average billed CPU cores for the previous complete hourly billing window:

```promql
sum(sealos_billing_resource_usage{resource="cpu", unit="1m"}) / 1000
```

Average billed CPU cores by owner:

```promql
sum by (owner) (sealos_billing_owner_resource_usage{resource="cpu", unit="1m"}) / 1000
```

Average billed CPU cores by owner and namespace:

```promql
sum by (owner, namespace) (sealos_billing_owner_resource_usage{resource="cpu", unit="1m"}) / 1000
```

Average billed memory MiB by owner:

```promql
sum by (owner) (sealos_billing_owner_resource_usage{resource="memory", unit="1Mi"})
```

Average billed storage MiB across the cluster:

```promql
sum(sealos_billing_resource_usage{resource="storage", unit="1Mi"})
```

Billed network MiB in the previous complete hourly billing window:

```promql
sum(sealos_billing_resource_usage{resource="network", unit="1Mi"})
```

Namespace billed CPU cores:

```promql
sum by (namespace, owner) (sealos_billing_owner_resource_usage{resource="cpu", unit="1m"}) / 1000
```

Collector freshness:

```promql
time() - sealos_billing_last_success_timestamp_seconds
```

## Cardinality Notes

Cluster-level metrics have low cardinality because they aggregate by resource
and unit. Owner-level metrics add owner and namespace cardinality for billed
records in the previous complete hourly window.
