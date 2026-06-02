# RegistryProxy Collector

RegistryProxy Collector 用于检测 Docker Registry 镜像代理的可用性。它通过 Docker Registry HTTP API 做两类检查：

- `GET /v2/`：确认 registry API 是否可访问。
- `GET /v2/<repository>/manifests/<reference>`：确认指定镜像 manifest 是否可以成功获取。

插件会先解析配置中的代理仓库地址，得到实际访问的 `scheme`、`host`、`port`、`repository`、`reference`、`info` 和可选固定 `ips`。每轮检查时，配置了 `ips` 的 registry 会按固定 IP 发起 API 和 manifest 请求；未配置 `ips` 的 registry 会解析当前域名使用的 IP 并逐个检查。最终指标会同时包含配置元信息、当前 IP、检查类型、HTTP 状态码和错误类型。

## 工作流程总览

```text
配置文件 / 环境变量
        |
        v
NewCollector 创建插件
        |
        v
加载 collectors.registryproxy 配置
        |
        v
解析 runtimeConfig
        |
        v
注册 Prometheus 指标描述符
        |
        v
Start 启动后台 pollLoop
        |
        v
首次 Poll 完成后 SetReady
        |
        v
每 checkInterval 周期重复检查
        |
        v
DNS 解析 endpoint host
        |
        v
按 IP 发起 /v2/ 和 manifest 请求
        |
        v
把最新结果写入内存缓存
        |
        v
Prometheus scrape 时从缓存生成指标
```

## 代码结构

| 文件 | 作用 |
|------|------|
| `config.go` | 定义 YAML 和环境变量可加载的配置结构，以及默认值 |
| `runtime.go` | 把原始配置解析成运行期结构，负责 endpoint、scheme、port、repository、reference 等字段规范化 |
| `checker.go` | 执行 DNS 解析、按 IP 拨号、访问 Docker Registry API 和 manifest |
| `registryproxy.go` | Collector 主体，负责轮询、缓存检查结果、暴露 Prometheus 指标 |
| `factory.go` | 注册插件并实现 `NewCollector` 生命周期创建逻辑 |
| `runtime_test.go` | 覆盖配置解析、endpoint 解析和 URL 构建 |
| `checker_test.go` | 覆盖本地 fake registry 的 API 和 manifest 检查行为 |

## 生命周期流程

### 1. 注册插件

插件在 `factory.go` 中通过 `init()` 自动注册：

```go
registry.MustRegister(collectorName, NewCollector)
```

`collectorName` 是 `registryproxy`。主程序引入 `pkg/collector/all` 后，会触发该包的 `init()`，从而把 `registryproxy` 注册到全局 collector registry。

配置中启用插件时使用：

```yaml
enabledCollectors:
  - registryproxy
```

### 2. 创建 Collector

当 server 初始化 enabled collectors 时，会调用 `NewCollector`。

创建流程：

1. 调用 `NewDefaultConfig()` 得到默认配置。
2. 使用 `factoryCtx.ConfigLoader.LoadModuleConfig("collectors.registryproxy", cfg)` 加载 YAML 和环境变量配置。
3. 调用 `newRuntimeConfig(cfg)` 把原始配置转换成运行期配置。
4. 创建 `base.BaseCollector`，并启用 `base.WithWaitReadyOnCollect(true)`。
5. 创建 `RegistryChecker`。
6. 注册 Prometheus 指标描述符。
7. 设置生命周期 hooks：`StartFunc`、`StopFunc`、`CollectFunc`。

`WithWaitReadyOnCollect(true)` 的作用是：Prometheus scrape 时，如果插件首次检查还没完成，collector 会等待 ready；如果超时还没 ready，就跳过本次采集，避免暴露未初始化的数据。

### 3. 启动后台轮询

插件启动后，`StartFunc` 会启动一个 goroutine：

```go
go c.pollLoop(ctx)
```

`pollLoop` 的行为：

1. 立即执行一次 `Poll(ctx)`。
2. 首次 Poll 完成后调用 `SetReady()`。
3. 创建 ticker，每隔 `checkInterval` 再执行一次 `Poll(ctx)`。
4. 收到 context cancel 后退出。

这和 `domain` 插件的轮询生命周期一致。

## 配置解析流程

### 输入配置

示例：

```yaml
collectors:
  registryproxy:
    registries:
      - endpoint: http://67.21.84.122:5000
        info: public mirror proxy
        repository: library/busybox
        reference: latest
        manifestAcceptHeader: application/vnd.docker.distribution.manifest.v2+json
      - endpoint: http://registry-proxy.example.com:5000
        info: internal registry proxy
        ips:
          - 10.0.0.12
          - 10.0.0.13
        repository: library/alpine
        reference: latest
        headers:
          X-Probe: sealos-state-metrics
    checkTimeout: 30s
    checkInterval: 1m
```

### 默认值

| 字段 | 默认值 |
|------|--------|
| `registries` | `[]` |
| `checkTimeout` | `30s` |
| `checkInterval` | `1m` |
| `registries[].manifestAcceptHeader` | `application/vnd.docker.distribution.manifest.v2+json` |
| `registries[].headers` | `{}` |
| `registries[].info` | `""` |
| `registries[].ips` | `[]` |

### registry 配置格式

`registries` 只支持对象数组。每一项都必须显式配置 `endpoint`、`repository` 和 `reference`。

```yaml
registries:
  - endpoint: http://registry-proxy.example.com:5000
    info: internal proxy
    ips:
      - 10.0.0.12
      - 10.0.0.13
    repository: library/busybox
    reference: latest
    manifestAcceptHeader: application/vnd.docker.distribution.manifest.v2+json
    headers:
      X-Probe: sealos-state-metrics
```

不再支持单字符串配置，例如 `- registry-proxy.example.com:5000`。这样可以避免隐式默认镜像导致误判，因为不是每个 registry proxy 都一定有 `library/busybox:latest`。

### endpoint 解析规则

支持以下形式：

```text
http://67.21.84.122:5000
http://registry-proxy.example.com:5000
https://registry-proxy.example.com:5443
http://[2001:db8::1]:5000
```

解析结果会得到：

| 运行期字段 | 来源 |
|------------|------|
| `target.Scheme` | `endpoint` 中的 URL scheme |
| `target.Host` | endpoint host |
| `target.Port` | endpoint port |
| `endpoint` | 原始配置值，作为指标 label 保留 |

当前只支持 `http` 和 `https`。`endpoint` 必须同时包含协议和端口，例如 `http://a.com:88`。如果 `endpoint` 中缺少协议、缺少端口，或者包含 path、query、fragment、user info，都会被认为是非法配置，因为插件会自行构造 `/v2/` 和 manifest API 路径。

### 固定 IP 配置

`ips` 用于指定实际拨号目标：

```yaml
registries:
  - endpoint: https://registry-proxy.example.com:5443
    ips:
      - 10.0.0.12
      - 10.0.0.13
    repository: library/busybox
    reference: latest
```

配置 `ips` 后，插件使用 `endpoint` 的 scheme、host 和 port 构造请求 URL，HTTP Host 和 HTTPS SNI 仍使用 `endpoint` 中的 host，TCP 连接拨到 `ips` 中的地址。`ips` 支持 IPv4 和 IPv6，重复值会被去重。

### 环境变量覆盖

支持以下环境变量：

| 环境变量 | 对应配置 |
|----------|----------|
| `COLLECTORS_REGISTRYPROXY_CHECK_TIMEOUT` | `checkTimeout` |
| `COLLECTORS_REGISTRYPROXY_CHECK_INTERVAL` | `checkInterval` |

示例：

```bash
COLLECTORS_REGISTRYPROXY_CHECK_TIMEOUT=10s
COLLECTORS_REGISTRYPROXY_CHECK_INTERVAL=1m
```

注意：

- `registries` 是结构化对象数组，只能通过 YAML 配置。
- `endpoint`、`ips`、`repository`、`reference`、`info`、`manifestAcceptHeader` 和 `headers` 不能通过环境变量覆盖。

## 单轮检查流程

每次 `Poll(ctx)` 会检查当前配置的所有 registry proxy。

### 1. 并发检查所有 registry proxy

`Poll` 会遍历 `runtime.registries`，为每个配置项启动一个 goroutine。

每个 goroutine 执行：

```text
RegistryChecker.CheckIPs(ctx, registry, logger)
```

检查结果先写入本轮临时 map。等所有 goroutine 完成后，再一次性替换 collector 内部缓存。这样 Prometheus scrape 时读到的始终是上一轮完整结果，不会读到半更新状态。

### 2. 选择检查 IP

如果 registry 配置了 `ips`，插件直接使用这组固定 IP。此时 `ResolveOk = true`，`IPCount` 等于固定 IP 数量，`sealos_registry_proxy_dns_status` 表示固定 IP 列表可用。

如果 registry 未配置 `ips`，插件执行 DNS 解析：

```text
resolveRegistryIPs(ctx, registry.target.Host, checkTimeout)
```

如果 host 本身就是 IP，例如 `67.21.84.122`，插件不会做 DNS 查询，而是直接把这个 IP 作为检查目标。

如果 host 是域名，例如 `registry-proxy.example.com`，插件会调用系统 resolver 做 `LookupHost`。解析到的每一个 IP 都会被单独检查。

DNS 失败时：

```text
ResolveOk = false
IPCount = 0
HealthyIPs = 0
UnhealthyIPs = 0
```

并且会产生一条 `sealos_registry_proxy_dns_status` 指标，`error_type` 为 `DNS`。

### 3. 按 IP 构造 HTTP client

插件不是简单访问 `http://host:port` 后让系统自己选择 IP，而是做了自定义拨号：

```text
请求 URL: http://registry-proxy.example.com:5000/v2/
实际拨号: <configured-or-resolved-ip>:5000
HTTP Host: registry-proxy.example.com:5000
```

这样可以同时满足两个目标：

- 指标中能明确知道当前检查的是哪个 IP。
- HTTP Host 和 TLS SNI 仍然保留原始 host，适配基于域名的 registry 服务。

对于 `https`：

- TCP 连接会拨到解析出的 IP。
- TLS `ServerName` 使用配置中的 host。
- 证书校验仍按 host 执行。

### 4. 检查 `/v2/` API

插件会请求：

```text
GET <scheme>://<host>:<port>/v2/
```

等价 curl：

```bash
curl -i http://67.21.84.122:5000/v2/
```

判定规则：

| HTTP 状态码 | 判定 |
|-------------|------|
| `2xx` | 成功 |
| `401` | 成功 |
| 其他状态码 | 失败 |
| 连接失败、超时、TLS 错误 | 失败 |

`401 Unauthorized` 被认为是成功，是因为 Docker Registry API 在需要认证时经常通过 `/v2/` 返回 `401`，这说明 registry API 本身是可达的。

检查结果写入：

```text
check_type = "api"
status_code = 实际 HTTP 状态码
error_type = 失败分类，成功时为空
```

### 5. 检查镜像 manifest

插件会请求：

```text
GET <scheme>://<host>:<port>/v2/<repository>/manifests/<reference>
Accept: <manifestAcceptHeader>
```

默认等价 curl：

```bash
curl -fsSL \
  -H 'Accept: application/vnd.docker.distribution.manifest.v2+json' \
  http://67.21.84.122:5000/v2/library/busybox/manifests/latest
```

判定规则：

| HTTP 状态码 | 判定 |
|-------------|------|
| `2xx` | 成功 |
| 非 `2xx` | 失败 |
| 连接失败、超时、TLS 错误 | 失败 |

manifest 检查比 `/v2/` 更严格，因为它要验证代理是否真的能拿到指定镜像元数据。比如 `/v2/` 可达但 manifest 返回 `404`，说明 registry 服务存在，但指定镜像无法通过该代理获取。

检查结果写入：

```text
check_type = "manifest"
status_code = 实际 HTTP 状态码
error_type = 失败分类，成功时为空
```

### 6. 聚合 registry 级健康状态

每个 IP 都完成 API 和 manifest 检查后，插件会聚合 registry 级状态：

```text
IPCount = 解析到的 IP 数量
HealthyIPs = API 和 manifest 都成功的 IP 数量
UnhealthyIPs = IPCount - HealthyIPs
```

一个 IP 必须同时满足：

```text
/v2/ API 成功
manifest 获取成功
```

才会被计入 `healthy_ips`。

### 7. 更新内存缓存

每轮 Poll 都会创建新的临时结果：

```text
newRegistries map[string]*RegistryHealth
newIPs        map[string]*IPHealth
```

所有 registry proxy 检查完成后，插件加锁并整体替换：

```text
c.registries = newRegistries
c.ips = newIPs
```

缓存 key 包含：

```text
endpoint + info + repository + reference
```

这可以避免同一个 endpoint 配置多组不同镜像检查时互相覆盖。

## 指标暴露流程

Prometheus scrape 时会调用 collector 的 `Collect`。

`Collect` 不会现场访问 registry，也不会做 DNS 查询；它只读取最近一次 Poll 写入的内存缓存并生成指标。

这样做的好处：

- scrape 请求不会被外部 registry 网络延迟拖慢。
- 所有外部探测都由后台 polling 控制。
- 指标暴露逻辑稳定，失败也只影响下一轮探测结果。

## 指标说明

### `sealos_registry_proxy_health`

registry proxy 聚合健康状态。

**类型：** Gauge

**Labels：**

| Label | 含义 |
|-------|------|
| `endpoint` | 配置中的 registry proxy endpoint |
| `scheme` | 实际请求 scheme |
| `host` | 解析后的 host |
| `port` | 解析后的 port |
| `info` | 配置中的 info 字段 |
| `repository` | manifest 检查使用的 repository |
| `reference` | manifest 检查使用的 tag 或 digest |
| `type` | 聚合指标类型 |

`type` 取值：

| type | 含义 |
|------|------|
| `resolve` | DNS 解析是否成功，成功为 `1`，失败为 `0` |
| `ip_count` | 当前解析到的 IP 数量 |
| `healthy_ips` | API 和 manifest 都成功的 IP 数量 |
| `unhealthy_ips` | API 或 manifest 失败的 IP 数量 |

示例：

```promql
sealos_registry_proxy_health{endpoint="http://67.21.84.122:5000",type="resolve"} 1
sealos_registry_proxy_health{endpoint="http://67.21.84.122:5000",type="ip_count"} 1
sealos_registry_proxy_health{endpoint="http://67.21.84.122:5000",type="healthy_ips"} 1
sealos_registry_proxy_health{endpoint="http://67.21.84.122:5000",type="unhealthy_ips"} 0
```

### `sealos_registry_proxy_dns_status`

DNS 解析状态。

**类型：** Gauge

**Labels：**

```text
endpoint, scheme, host, port, info, repository, reference, error_type
```

值：

| 值 | 含义 |
|----|------|
| `1` | DNS 解析成功 |
| `0` | DNS 解析失败或没有可用 IP |

### `sealos_registry_proxy_status`

每个 IP、每种检查类型的状态。

**类型：** Gauge

**Labels：**

| Label | 含义 |
|-------|------|
| `endpoint` | 配置中的 registry proxy endpoint |
| `scheme` | 实际请求 scheme |
| `host` | 解析后的 host |
| `port` | 解析后的 port |
| `info` | 配置中的 info 字段 |
| `repository` | manifest 检查使用的 repository |
| `reference` | manifest 检查使用的 tag 或 digest |
| `ip` | 当前检查实际拨号使用的 IP |
| `check_type` | `api` 或 `manifest` |
| `status_code` | HTTP 状态码；请求未收到响应时为 `0` |
| `error_type` | 错误分类；成功时为空 |

值：

| 值 | 含义 |
|----|------|
| `1` | 该检查成功 |
| `0` | 该检查失败 |

示例：

```promql
sealos_registry_proxy_status{
  endpoint="http://registry-proxy.example.com:5000",
  ip="10.0.0.12",
  check_type="api",
  status_code="200",
  error_type=""
} 1

sealos_registry_proxy_status{
  endpoint="http://registry-proxy.example.com:5000",
  ip="10.0.0.12",
  check_type="manifest",
  status_code="404",
  error_type="HTTPStatus"
} 0
```

### `sealos_registry_proxy_response_time_seconds`

每个 IP、每种检查类型的请求耗时。

**类型：** Gauge

**Labels：**

```text
endpoint, scheme, host, port, info, repository, reference, ip, check_type
```

该指标记录 HTTP client 发起请求后的耗时。只要请求进入 HTTP client 并产生耗时，就可能被记录；DNS 失败不会产生该指标。

## 错误分类

`error_type` 可能取值：

| error_type | 含义 |
|------------|------|
| `DNS` | DNS 查询失败 |
| `NoIP` | DNS 没有返回可用 IP |
| `Timeout` | 请求超时或上下文 deadline exceeded |
| `Connect` | 连接失败，例如 connection refused、no route to host |
| `HTTPStatus` | 收到了 HTTP 响应，但状态码不符合成功判定 |
| `Request` | 其他请求构造或执行错误 |
| `Context` | context canceled 或 context deadline exceeded |
| 空字符串 | 成功 |

## 典型故障判断

### DNS 失败

表现：

```promql
sealos_registry_proxy_health{type="resolve"} == 0
sealos_registry_proxy_dns_status == 0
```

含义：

插件无法把配置中的 `host` 解析成 IP。可能原因包括域名错误、DNS 服务异常、集群 DNS 配置异常。

### API 可达，但 manifest 不可用

表现：

```promql
sealos_registry_proxy_status{check_type="api"} == 1
sealos_registry_proxy_status{check_type="manifest"} == 0
```

含义：

registry 服务本身可访问，但指定镜像 manifest 获取失败。常见原因：

- 代理没有同步该镜像。
- 上游 registry 不可访问。
- repository 或 reference 配置错误。
- 需要认证但未配置 headers。

### 某些 IP 失败

表现：

```promql
sealos_registry_proxy_health{type="unhealthy_ips"} > 0
```

再按 IP 查看：

```promql
sealos_registry_proxy_status{endpoint="http://registry-proxy.example.com:5000"}
```

含义：

同一个域名解析到多个 IP，其中部分 IP 后端异常。因为插件按 IP 发起检查，所以可以定位到具体不可用的 IP。

## 常用 PromQL

查看不可用代理：

```promql
sealos_registry_proxy_health{type="healthy_ips"} == 0
```

查看 DNS 失败：

```promql
sealos_registry_proxy_dns_status == 0
```

查看 manifest 失败：

```promql
sealos_registry_proxy_status{check_type="manifest"} == 0
```

查看 API 失败：

```promql
sealos_registry_proxy_status{check_type="api"} == 0
```

查看每个代理的健康 IP 比例：

```promql
sealos_registry_proxy_health{type="healthy_ips"}
/
sealos_registry_proxy_health{type="ip_count"}
```

查看 manifest 请求耗时：

```promql
sealos_registry_proxy_response_time_seconds{check_type="manifest"}
```

## 与手工 curl 的对应关系

插件配置：

```yaml
collectors:
  registryproxy:
    registries:
      - endpoint: http://67.21.84.122:5000
        repository: library/busybox
        reference: latest
        manifestAcceptHeader: application/vnd.docker.distribution.manifest.v2+json
```

对应 API 检查：

```bash
curl -i http://67.21.84.122:5000/v2/
```

对应 manifest 检查：

```bash
curl -fsSL \
  -H 'Accept: application/vnd.docker.distribution.manifest.v2+json' \
  http://67.21.84.122:5000/v2/library/busybox/manifests/latest
```

如果 endpoint 是域名，插件会比普通 curl 多做一步：它会把域名解析出的每个 IP 都分别检查，并把实际使用的 IP 放进指标 label。

## 注意事项

- 该插件只检测 registry API 和 manifest 获取能力，不会拉取镜像层 blob。
- manifest 检查的镜像建议选择小而稳定的公共镜像，例如 `library/busybox:latest`，也可以替换成业务上必须可用的镜像。
- 如果 registry proxy 需要认证，可以通过 `headers` 配置静态 HTTP header。
- 如果使用 `https`，插件会按配置的 host 做 TLS 校验和 SNI。当前没有提供跳过 TLS 校验的配置。
- `info` 会原样作为指标 label 输出，建议保持短小、稳定，避免高基数。
- `repository` 和 `reference` 也会作为 label 输出，不建议配置大量不同镜像组合，否则会增加指标基数。
