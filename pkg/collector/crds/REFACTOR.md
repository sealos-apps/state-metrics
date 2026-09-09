# CRDs Collector 重构说明

本文档说明 `pkg/collector/crds` 本次性能重构的背景、改动范围、完整交互链路、运行时工作流程、并发模型以及后续扩展方式。

## 背景

重构前，CRDs collector 的核心思路是：

1. dynamic informer watch Kubernetes CR 对象。
2. informer event handler 将完整 CR 对象写入 `ResourceStore`。
3. Prometheus 每次 scrape 时，collector 调用 `ResourceStore.List()` 取出所有 CR。
4. `List()` 在默认开启 deep copy 的情况下，会复制所有 CR。
5. collector 在 scrape 阶段逐个 CR 提取字段、组装 label、生成 metric。
6. `count` 类型还会对所有 CR 再做一次聚合扫描。

这个模型的问题是，重活都发生在 scrape 路径上。只要 CR 数量较多或者 CR 对象较大，每次 Prometheus 拉取都会触发大量对象复制、字段解析和 metric 组装，容易导致 scrape 超时。

本次重构的目标是：

- 从“缓存完整 CR，scrape 时组装”改为“informer 事件发生时就更新最终 metric 结果”。
- scrape 路径只输出已经准备好的 `prometheus.Metric`。
- 不再保留完整 CR 缓存，减少内存占用和 deep copy 开销。
- 保持现有配置方式不变。
- 让新增 metric 类型仍然有清晰、集中、可维护的扩展点。

## 改动范围

主要改动文件：

| 文件 | 作用 |
| --- | --- |
| `pkg/collector/crds/metric_store.go` | 新增派生 metric 缓存，实现配置编译、事件增量更新、count 聚合、scrape 快照输出 |
| `pkg/collector/crds/crd_collector.go` | 简化 collector，`Collect` 只从 `metricStore` 输出已生成 metric |
| `pkg/collector/crds/informer/informer.go` | informer 从写入完整 CR store 改为写入事件接口；增加 transform 只保留配置需要的字段；支持单 namespace 过滤 |
| `pkg/collector/crds/factory.go` | 构造 `metricStore`，将配置编译结果和需要保留的字段路径传给 informer |
| `pkg/collector/crds/metric_store_test.go` | 新增单元测试，覆盖 add/update/delete 对普通 metric 和 count 聚合的增量更新 |
| `pkg/collector/crds/store/store.go` | 删除旧的完整 CR 缓存实现 |

配置文件不需要修改。现有 `collectors.crds.crds` 配置继续兼容。

## 新旧链路对比

### 旧链路

```text
Kubernetes API Server
  -> dynamic informer list/watch
  -> event handler
  -> ResourceStore 缓存完整 CR 对象
  -> Prometheus scrape
  -> ResourceStore.List()
  -> deep copy 所有 CR
  -> 每个 CR 提取字段并生成 metric
  -> count metric 再扫描所有 CR 聚合
  -> 输出 prometheus.Metric
```

特点：

- informer 阶段轻，scrape 阶段重。
- 每次 scrape 都重复解析相同 CR。
- `List()` 会复制所有对象，CR 越大开销越明显。
- count 类型每次 scrape 都全量重新聚合。

### 新链路

```text
Kubernetes API Server
  -> dynamic informer list/watch
  -> informer transform 裁剪对象，只保留配置需要字段
  -> event handler
  -> metricStore 根据事件增量生成或删除最终 metric
  -> Prometheus scrape
  -> metricStore 快照当前 metric 引用
  -> 输出 prometheus.Metric
```

特点：

- informer 事件阶段做字段解析和 metric 组装。
- scrape 阶段不再解析 CR，不再 deep copy CR，不再全量聚合 count。
- count 类型在 add/update/delete 时维护增量计数。
- informer 内部缓存的对象也被裁剪，避免保留完整 CR。

## 核心组件

### `CrdCollector`

`CrdCollector` 现在只负责 Prometheus collector 层面的事情：

- 保存 descriptor。
- 检查 informer 是否已经 synced。
- 调用 `metricStore.Collect(ch)` 输出 metric。

关键流程：

```go
func (c *CrdCollector) Collect(ch chan<- prometheus.Metric) {
	if !c.informer.HasSynced() {
		c.logger.Warn("Informer cache not synced, skipping collection")
		return
	}

	c.store.Collect(ch)
}
```

这里不再调用 `List()`，不再读取 CR 对象，也不再做字段提取。

### `metricStore`

`metricStore` 是新链路的核心。它存储的不是 CR，而是由 CR 派生出来的 metric 结果。

核心字段：

```go
type metricStore struct {
	mu sync.RWMutex

	commonLabelPaths []string
	metrics          []compiledMetric
	descriptors      map[string]*prometheus.Desc
	namespaces       map[string]struct{}

	objects      map[string]objectMetrics
	countValues  map[string]map[string]int
	countMetrics map[string]map[string]prometheus.Metric
}
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `commonLabelPaths` | 预先按 label name 排序后的 common label 字段路径 |
| `metrics` | 编译后的 metric 配置，包含 descriptor、label path、condition 字段名等 |
| `descriptors` | Prometheus descriptor，按 metric name 保存 |
| `namespaces` | namespace 过滤集合，空集合表示不过滤 |
| `objects` | 每个对象当前派生出的非 count metric，以及该对象对 count 的贡献 |
| `countValues` | count metric 的原始计数，结构为 `metricName -> value -> count` |
| `countMetrics` | count metric 已生成的 `prometheus.Metric`，结构为 `metricName -> value -> metric` |

### `compiledMetric`

`compiledMetric` 是 `MetricConfig` 的运行时版本。

配置加载后，`newMetricStore` 会把 `MetricConfig` 编译成 `compiledMetric`：

- 构造 descriptor。
- 固定 label 顺序。
- 提前排序 info label path。
- 提前解析 condition 字段名默认值。
- 处理 `ValueLabel`、`KeyLabel` 的默认 label 名。

这样事件处理时不用重复做这些准备工作。

### `Informer`

`Informer` 不再依赖旧的 `ResourceStore`，而是依赖一个更小的事件接口：

```go
type ResourceEventStore interface {
	Add(obj *unstructured.Unstructured)
	Update(obj *unstructured.Unstructured)
	Delete(obj *unstructured.Unstructured)
	Len() int
}
```

当前实现中，`metricStore` 实现了这个接口。

这让 informer 的职责更单纯：只负责 watch Kubernetes 对象，并把事件转发给后面的派生状态存储。后续如果要做别的派生缓存，也可以实现同一个接口。

## 启动流程

完整启动链路从 `NewCollector` 开始。

### 1. 加载配置

`factory.go` 中仍然先创建默认配置，再从配置加载器加载：

```go
cfg := NewDefaultConfig()
factoryCtx.ConfigLoader.LoadModuleConfig("collectors.crds", cfg)
```

如果没有配置任何 CRD，collector 仍然返回 `nil`，表示禁用。

### 2. 创建 Kubernetes dynamic client

CRDs collector 继续使用 dynamic client：

```go
restConfig, err := factoryCtx.GetRestConfig()
dynamicClient, err := createDynamicClient(restConfig)
```

这部分没有改变。

### 3. 为每个 CRD 创建 `metricStore`

每个 `CRDConfig` 会创建一个独立 `metricStore`：

```go
metricStore, err := newMetricStore(
	crdCfg,
	"",
	factoryCtx.Logger.WithField("crd", crdCfg.Name),
)
```

这里会立即根据配置构造最终结果需要的运行时结构：

- descriptor。
- metric 类型分发信息。
- label 顺序。
- common label 字段路径。
- info label 字段路径。
- condition 字段名。
- namespace 过滤集合。
- count 聚合缓存结构。

这一步就是“根据配置文件构造出最终结果”的核心入口。

### 4. 计算 informer 需要保留的字段路径

`metricStore.RequiredPaths()` 会返回当前配置中真正需要读取的字段路径：

- `commonLabels` 中的 path。
- 每个 metric 的 `path`。
- info metric 的额外 label path。

这些路径会传给 informer：

```go
informerConfig := informer.InformerConfig{
	GVR:           ...,
	ResyncPeriod:  crdCfg.ResyncPeriod,
	Namespaces:    crdCfg.Namespaces,
	TransformPaths: metricStore.RequiredPaths(),
}
```

### 5. 创建 informer

informer 使用 `metricStore` 作为事件接收方：

```go
i, err := informer.NewInformer(
	dynamicClient,
	&informerConfig,
	metricStore,
	logger,
)
```

### 6. 创建 `CrdCollector`

`CrdCollector` 也使用同一个 `metricStore`：

```go
crdCollector, err := NewCrdCollector(
	crdCfg,
	metricStore,
	i,
	"",
	logger,
)
```

最终关系是：

```text
CrdCollector
  -> informer: 用于 HasSynced 健康检查
  -> metricStore: 用于输出已生成 metric

Informer
  -> metricStore: 用于 Add/Update/Delete 增量更新派生结果
```

## informer 运行流程

collector 生命周期启动时，会调用每个 CRD collector 的 informer：

```text
BaseCollector.Start
  -> CRDs Collector StartFunc
  -> for each crdCollector
  -> crdCollector.informer.Start()
  -> collect all crdCollector.informer.HasSynced funcs
  -> cache.WaitForCacheSync(ctx.Done(), syncFuncs...)
  -> c.SetReady()
```

### 1. 创建 informer factory

informer 根据 namespace 配置创建 factory：

```go
namespace := i.factoryNamespace()
factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
	i.dynamicClient,
	i.config.ResyncPeriod,
	namespace,
	nil,
)
```

namespace 行为：

- `namespaces` 为空：watch 所有 namespace。
- `namespaces` 只有一个非空值：直接使用 namespaced informer，让 Kubernetes API 层过滤。
- `namespaces` 有多个值：当前仍 watch 所有 namespace，再由 `metricStore` 本地过滤。

这里选择保守实现，是因为 client-go 的 dynamic informer factory 一次只接受一个 namespace。多 namespace 如果要 API 层过滤，需要为每个 namespace 建 informer，复杂度更高，可以作为后续优化。

### 2. 设置 transform

如果存在 `TransformPaths`，informer 会设置 transform：

```go
i.informer.SetTransform(i.transformObject)
```

`transformObject` 会创建一个新的 `unstructured.Unstructured`，只保留：

- `metadata.name`
- `metadata.namespace`
- `metadata.uid`
- `metadata.resourceVersion`
- `metadata.generation`
- 配置中实际使用的字段路径

示例：

配置：

```yaml
commonLabels:
  name: metadata.name
  namespace: metadata.namespace
metrics:
  - type: gauge
    name: app_replicas
    path: spec.replicas
  - type: string_state
    name: app_phase
    path: status.phase
```

transform 后 informer 缓存中的对象只需要类似：

```yaml
metadata:
  name: app-1
  namespace: default
  uid: ...
  resourceVersion: ...
spec:
  replicas: 3
status:
  phase: Running
```

CR 中其他字段不会进入 informer 缓存。

### 3. 注册事件处理

事件处理保持 Kubernetes informer 的标准模型：

```text
AddFunc    -> handleAdd    -> metricStore.Add(obj)
UpdateFunc -> handleUpdate -> metricStore.Update(newObj)
DeleteFunc -> handleDelete -> metricStore.Delete(obj)
```

update 事件仍会比较 `resourceVersion`，如果没有变化则跳过：

```go
if oldObj.GetResourceVersion() == newObj.GetResourceVersion() {
	return
}
```

### 4. 统一等待 informer sync

`Informer.Start()` 只负责创建 informer、注册 event handler 并启动 informer goroutine，不在单个 informer 内部等待 cache sync。

`factory.go` 中的 CRDs collector lifecycle 会先启动所有 CRD informer，再统一等待所有 cache sync：

```go
syncFuncs := make([]cache.InformerSynced, 0, len(c.crdCollectors))
for _, crdCollector := range c.crdCollectors {
	if err := crdCollector.informer.Start(); err != nil {
		return err
	}

	syncFuncs = append(syncFuncs, crdCollector.informer.HasSynced)
}

if !cache.WaitForCacheSync(ctx.Done(), syncFuncs...) {
	return errors.New("failed to sync CRD informer caches")
}
```

sync 完成后，BaseCollector 才会 `SetReady()`。

这保证 Prometheus scrape 时看到的是 informer 初始 list 已经处理后的派生 metric 状态。

这样做还有一个性能收益：多个 CRD informer 的初始 list/watch 可以并发启动，避免旧的“启动第一个并等待完成，再启动第二个”的串行同步。

## Add 事件工作流程

以新增一个 CR 为例。

```text
Kubernetes API Server
  -> watch event: ADDED
  -> informer transformObject
  -> AddFunc
  -> handleAdd
  -> metricStore.Add
  -> metricStore.upsert
  -> buildObjectMetrics
  -> 写入 objects 和 count 聚合
```

`metricStore.Add` 实际调用 `upsert`：

```go
func (s *metricStore) Add(obj *unstructured.Unstructured) {
	s.upsert(obj)
}
```

`upsert` 的核心步骤：

1. 根据对象 namespace/name 计算 key。
2. 在锁外调用 `buildObjectMetrics(obj)`，从对象中提取字段并生成新的 metric。
3. 加写锁。
4. 如果旧对象已经存在，先删除旧对象对 count 的贡献。
5. 写入新对象的普通 metric。
6. 写入新对象对 count 的贡献。
7. 更新 add/update 统计。

这里有一个重要优化：字段解析和 metric 构造在加锁前完成，减少写锁持有时间。

## Update 事件工作流程

update 和 add 共享 `upsert`。

```text
Kubernetes API Server
  -> watch event: MODIFIED
  -> informer transformObject
  -> UpdateFunc
  -> handleUpdate
  -> 如果 resourceVersion 未变化则跳过
  -> metricStore.Update
  -> metricStore.upsert
  -> 先移除旧派生结果
  -> 再写入新派生结果
```

为什么先删除旧结果再写新结果：

- 普通 metric：对象的 label 或 value 可能变化，需要整体替换。
- count metric：对象旧状态可能是 `Running`，新状态可能是 `Pending`，必须从旧 bucket 减 1，再给新 bucket 加 1。

示例：

```text
旧对象 status.phase = Running
新对象 status.phase = Pending

update 前:
  app_phase_count{value="Running"} 1

update 后:
  app_phase_count{value="Pending"} 1
```

如果还有其他对象处于 `Running`，则只是对应 count 减 1，不会删除整个 value。

## Delete 事件工作流程

delete 事件只需要删除对象当前保存的派生结果：

```text
Kubernetes API Server
  -> watch event: DELETED
  -> DeleteFunc
  -> handleDelete
  -> metricStore.Delete
  -> removeLocked
  -> 删除普通 metric
  -> 删除 count 贡献
```

delete 不需要重新解析 CR，因为 `metricStore.objects[key]` 中已经保存了该对象上一次贡献过的 metric 和 count 信息。

这点对性能很重要：删除路径不依赖完整 CR，也不需要字段提取。

## Scrape 工作流程

Prometheus 拉取指标时，链路如下：

```text
Prometheus
  -> HTTP /metrics
  -> prometheus registry
  -> BaseCollector.Collect
  -> CRDs Collector collect
  -> CrdCollector.Collect
  -> informer.HasSynced()
  -> metricStore.Collect(ch)
  -> 输出已缓存 prometheus.Metric
```

`metricStore.Collect` 内部不会遍历 CR，不会解析字段：

```text
metricStore.Collect
  -> snapshotMetrics
  -> 在读锁内复制 prometheus.Metric 引用到 slice
  -> 释放读锁
  -> 将 slice 中的 metric 发送到 channel
```

这里特意不在持锁期间向 channel 发送 metric，原因是 channel 发送可能被 Prometheus registry 调度影响。如果一直持有读锁，informer 的 add/update/delete 写锁会被 scrape 阻塞。现在只在锁内做一次很快的引用快照，随后释放锁再发送。

## metric 类型处理

### `info`

事件处理时：

1. 提取 common label。
2. 按 label name 排序后的顺序提取 `labels` 中配置的字段。
3. 生成 value 固定为 1 的 Gauge metric。

scrape 时直接输出缓存好的 metric。

### `gauge`

事件处理时：

1. 提取 common label。
2. 从 `path` 提取数值。
3. 使用 `helpers.ToFloat64` 转换为 float64。
4. 生成 Gauge metric。

支持原本已有的 int、float、bool、string 数字转换逻辑。

### `string_state`

事件处理时：

1. 提取 common label。
2. 从 `path` 提取字符串。
3. 追加状态 label，默认 label 名为 `state`。
4. 生成 value 为 1 的 Gauge metric。

本次重构还保留了 `ValueLabel` 扩展点：如果配置了 `valueLabel`，会使用配置值作为状态 label 名；否则默认 `state`。

### `map_state`

事件处理时：

1. 从 `path` 提取 map。
2. 遍历 map entry。
3. 从 entry 中按 `valuePath` 取状态字符串。
4. 生成 label：common labels + key label + state label。
5. value 固定为 1。

默认 key label 是 `key`，默认 state label 是 `state`。如果配置了 `keyLabel` 或 `valueLabel`，会使用配置值。

### `map_gauge`

事件处理时：

1. 从 `path` 提取 map。
2. 遍历 map entry。
3. 从 entry 中按 `valuePath` 取数值。
4. 生成 label：common labels + key label。
5. value 为提取到的数值。

默认 key label 是 `key`，可通过 `keyLabel` 覆盖。

### `conditions`

事件处理时：

1. 从 `path` 提取 conditions slice。
2. 每个 condition 中读取 type/status/reason。
3. 字段名默认是 `type`、`status`、`reason`。
4. 如果配置了 `condition.typeField/statusField/reasonField`，使用配置值。
5. 当 status 等于 `true` 时 value 为 1，否则为 0。

### `count`

`count` 是最明显的性能改动点。

旧实现：

```text
每次 scrape:
  遍历所有 CR
  提取 path 字段
  用 map 重新统计 value -> count
  输出结果
```

新实现：

```text
Add:
  提取 path 字段
  countValues[metricName][value]++
  重建该 value 对应的 prometheus.Metric

Update:
  删除旧对象贡献:
    countValues[metricName][oldValue]--
  添加新对象贡献:
    countValues[metricName][newValue]++

Delete:
  删除旧对象贡献:
    countValues[metricName][oldValue]--

Scrape:
  直接输出 countMetrics 中已生成的 prometheus.Metric
```

如果某个 value 的 count 变为 0，则删除该 value 对应的 metric。这样不会输出 0 值的旧 bucket，行为与旧实现一致，因为旧实现只会输出当前存在的 value。

## 并发模型

`metricStore` 使用一个 `sync.RWMutex` 保护内部状态。

### 写路径

写路径包括：

- `Add`
- `Update`
- `Delete`

`Add` 和 `Update` 会在加锁前构造 `objectMetrics`，然后短时间持有写锁完成替换和 count 聚合。

这样设计的原因：

- 字段提取和 `prometheus.MustNewConstMetric` 相对更重。
- 它们不需要访问 `metricStore` 可变状态。
- 放在锁外可以减少对 scrape 快照和其他 informer 事件的阻塞。

### 读路径

读路径主要是 `Collect`。

`Collect` 会：

1. 获取读锁。
2. 把当前所有 `prometheus.Metric` 引用复制到临时 slice。
3. 释放读锁。
4. 遍历临时 slice，发送到 Prometheus channel。

这样避免了在 channel 发送期间持锁。

### Prometheus metric 对象复用

`metricStore` 缓存的是 `prometheus.Metric` 接口值。每次对象变化时，会生成新的 metric 替换旧 metric。scrape 时只是读取当前快照。

这里没有在 metric 对象上做原地修改，所以不会出现同一个 metric 被更新线程和 scrape 线程同时修改的问题。

## 内存模型

旧模型内存占用主要有两份：

1. client-go informer 内部缓存完整 CR。
2. `ResourceStore` 再缓存一份完整 CR，默认 deep copy。

新模型：

1. informer transform 后只缓存配置需要的字段。
2. `metricStore` 只缓存派生后的 `prometheus.Metric` 和少量 count 状态。
3. 不再有独立完整 CR store。

这减少了两类内存开销：

- CR 对象本身的重复保存。
- scrape 时 `List()` 带来的临时 deep copy。

## namespace 过滤

配置中的 `namespaces` 现在有两层作用：

1. informer factory 层面：
   - 如果只配置了一个 namespace，直接构造 namespaced informer。
   - 这样 Kubernetes API list/watch 返回的数据更少。
2. `metricStore` 层面：
   - 无论 informer 是否已经过滤，`metricStore` 都会再检查 namespace。
   - 这保证多 namespace 场景下也不会写入不需要的 metric。

多 namespace 当前仍然使用集群级 informer，再在本地过滤。后续如果要继续优化，可以把一个 CRDConfig 拆成多个 namespace informer，共享同一个 `metricStore`。

## 完整数据流示例

配置：

```yaml
collectors:
  crds:
    crds:
      - name: devbox
        gvr:
          group: devbox.sealos.io
          version: v1alpha2
          resource: devboxes
        namespaces: []
        commonLabels:
          name: metadata.name
          namespace: metadata.namespace
        metrics:
          - type: info
            name: devbox_info
            help: Devbox information
            labels:
              image: spec.image
          - type: count
            name: devbox_phase_count
            help: Devbox phase count
            path: status.phase
          - type: string_state
            name: devbox_status
            help: Devbox status
            path: status.phase
```

### 启动时

```text
newMetricStore(devbox config)
  -> commonLabelPaths = [
       "metadata.name",
       "metadata.namespace",
     ]
  -> compile info:
       descriptor = sealos_devbox_info
       labels = [name, namespace, image]
       labelPaths = [spec.image]
  -> compile count:
       descriptor = sealos_devbox_phase_count
       labels = [value]
  -> compile string_state:
       descriptor = sealos_devbox_status
       labels = [name, namespace, state]
  -> RequiredPaths = [
       metadata.name,
       metadata.namespace,
       spec.image,
       status.phase,
     ]
```

### informer 收到对象

原始 CR：

```yaml
metadata:
  name: devbox-1
  namespace: ns-a
spec:
  image: ubuntu:22.04
  extraLargeField: ...
status:
  phase: Running
  detail: ...
```

transform 后：

```yaml
metadata:
  name: devbox-1
  namespace: ns-a
  uid: ...
  resourceVersion: ...
spec:
  image: ubuntu:22.04
status:
  phase: Running
```

`metricStore` 生成：

```text
objects["ns-a/devbox-1"].series:
  sealos_devbox_info{name="devbox-1",namespace="ns-a",image="ubuntu:22.04"} 1
  sealos_devbox_status{name="devbox-1",namespace="ns-a",state="Running"} 1

objects["ns-a/devbox-1"].counts:
  {metricName: "devbox_phase_count", value: "Running"}

countValues:
  devbox_phase_count:
    Running: 1

countMetrics:
  sealos_devbox_phase_count{value="Running"} 1
```

### Prometheus scrape

scrape 不读取 CR，只输出：

```text
sealos_devbox_info{name="devbox-1",namespace="ns-a",image="ubuntu:22.04"} 1
sealos_devbox_status{name="devbox-1",namespace="ns-a",state="Running"} 1
sealos_devbox_phase_count{value="Running"} 1
```

## 扩展方式

如果以后要新增一个 metric 类型，通常需要改三个位置。

### 1. `compileMetric`

在 `metric_store.go` 的 `compileMetric` 中增加新类型：

- 定义 label names。
- 设置默认 label 名。
- 预处理该类型需要的字段名或配置。
- 创建 descriptor。

### 2. `RequiredPaths`

如果新类型除了 `metric.config.Path` 和 info labels 以外，还需要读取额外字段，需要把这些路径加入 `RequiredPaths()`。

这样 informer transform 才会保留对应字段。

### 3. `buildObjectMetrics`

在 `buildObjectMetrics` 的 switch 中增加新类型，调用新的 builder 方法生成 `prometheus.Metric` 或 count contribution。

建议新增独立方法，例如：

```go
func (s *metricStore) buildXxxMetric(
	obj *unstructured.Unstructured,
	metric compiledMetric,
	commonLabels []string,
) prometheus.Metric {
	...
}
```

### 4. 测试

在 `metric_store_test.go` 中补充测试：

- add 时是否生成正确 metric。
- update 时旧 metric 是否被替换。
- delete 时 metric 是否消失。
- 如果是聚合类型，验证旧 bucket 减少、新 bucket 增加。

## 行为保持和细节变化

保持不变：

- 配置格式保持不变。
- metric name 构造仍使用 `prometheus.BuildFQName(metricPrefix, "", metricCfg.Name)`。
- 默认 metric prefix 仍是 `sealos`。
- common label 和 info label 仍按 label name 排序，保证 label 顺序稳定。
- `conditions` 中 status 等于 true 时 value 为 1，否则为 0。
- `count` 只统计非空 value。

细节增强：

- `string_state` 的状态 label 默认是 `state`，但现在会尊重 `valueLabel`。
- `map_state` 的 key label 和 state label 会尊重 `keyLabel`、`valueLabel`。
- `map_gauge` 的 key label 会尊重 `keyLabel`。

需要注意：

- `helpers` 当前使用点分隔路径，例如 `status.phase`。复杂 JSONPath 表达式不是这次重构范围。
- `map_state` 和 `map_gauge` 的 `valuePath` 当前仍按 map entry 的直接 key 读取，不是嵌套路径解析。
- 多 namespace 配置当前不是多个 API 层 namespaced informer，而是集群 watch 后本地过滤。
- `count` bucket 归零后不会输出 0 值 metric，这与旧的每次全量扫描行为一致。

## 为什么删除旧 `store`

旧 `pkg/collector/crds/store` 的职责是保存完整 CR，并提供 `List/Get/ListByNamespace` 等方法。本次重构后没有任何代码继续引用它。

保留旧 store 的风险：

- 后续维护者可能误以为 CRDs collector 仍需要完整 CR 缓存。
- 容易在新增功能时重新引入 scrape 全量扫描。
- 增加维护成本。

因此本次直接删除旧实现，让代码结构表达新的设计：CRDs collector 不再缓存完整 CR，只缓存派生后的 metric 状态。

## 性能收益总结

| 路径 | 重构前 | 重构后 |
| --- | --- | --- |
| informer add/update | 写入完整 CR | 生成该对象最终 metric，并更新 count |
| informer delete | 删除完整 CR | 删除该对象派生 metric 和 count 贡献 |
| informer 内部缓存 | 完整 CR | transform 后的最小字段对象 |
| collector store | 完整 CR，通常 deep copy | `prometheus.Metric` 和 count 状态 |
| scrape | 全量 list、deep copy、字段解析、count 聚合 | 快照 metric 引用并输出 |
| count | 每次 scrape 全量重算 | add/update/delete 增量维护 |

对超时问题影响最大的点是：scrape 路径不再随 CR 对象大小和字段解析成本线性增长，只随最终 metric 数量增长。

## 验证

本次重构后已执行：

```bash
go test ./pkg/collector/crds/...
go test ./...
```

新增测试覆盖了：

- 新增两个对象后，普通 metric 和 count metric 正确生成。
- 对象状态从 `Running` 更新为 `Pending` 后，旧 count bucket 被移除，新 bucket 计数正确增加。
- 删除对象后，对应 metric 和 count 贡献正确移除。

## 后续可选优化

当前重构已经把主要瓶颈从 scrape 路径移走。后续如果 CR 数量继续增大，可以继续考虑：

1. 多 namespace 配置时，为每个 namespace 创建独立 informer，共享同一个 `metricStore`，进一步减少 API 返回数据。
2. 对 metric snapshot 做稳定排序，方便测试和 debug。Prometheus 本身不要求输出顺序稳定。
3. 为 `metricStore` 暴露更详细的内部统计，例如当前 object 数、series 数、count bucket 数。
4. 给 `map_state/map_gauge` 的 `valuePath` 支持嵌套路径。
5. 增加 benchmark，对比旧实现和新实现在大 CR、大对象数量下的 scrape 耗时。
