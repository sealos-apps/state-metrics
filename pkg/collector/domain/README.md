# Domain Collector

The Domain collector monitors domain health by performing DNS lookups or fixed-IP checks, HTTP checks, and certificate validation.

## Features

- **Domain-level metrics**: Aggregate health status for each domain
- **IP-level metrics**: Detailed health status for each resolved or configured IP
- **DNS resolution tracking**: Monitors DNS resolution success and IP counts when fixed IPs are not configured
- **Failure exposure**: DNS resolution failures and empty IP lists are exposed as unhealthy metrics
- **Concurrent checks**: Checks multiple domains concurrently for efficiency
- **Error classification**: Categorizes errors for better alerting and debugging

## Configuration

### YAML Configuration

```yaml
collectors:
  domain:
    domains:
      - endpoint: example.com
        skipTLSVerify: false
        followHTTPRedirects: true
      - endpoint: internal.example.local:8443
        ips:
          - 10.0.0.12
          - 10.0.0.13
        skipTLSVerify: true
        followHTTPRedirects: false
        path: /healthz?ready=true
        method: GET
        headers:
          X-Probe: sealos-state-metrics
        expectedStatusCodes: [200, 204]
      - api.example.com
    checkTimeout: "30s"
    checkInterval: "1m"
    dialRetries: 3
    includeIPv4: true
    includeIPv6: true
    includeCertCheck: true
    includeHTTPCheck: true
```

`domains` supports mixed entries:

- Legacy string entries such as `example.com` or `api.example.com:8443`
- Object entries such as `{ endpoint: internal.example.local:8443, ips: [10.0.0.12], skipTLSVerify: true, followHTTPRedirects: false, path: /healthz, method: GET, expectedStatusCodes: [200, 204] }`

### Configuration Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `domains` | `[]string` or `[]object` | `[]` | List of domains to monitor. Entries may be strings or objects with per-domain options |
| `domains[].endpoint` | string | - | Domain endpoint in `host` or `host:port` format |
| `domains[].ips` | []string | `[]` | Fixed IPs to check for this domain. When set, DNS lookup is skipped for this domain |
| `domains[].skipTLSVerify` | bool | `false` | Skip TLS certificate verification for this domain during HTTPS and cert checks |
| `domains[].followHTTPRedirects` | bool | `true` | Follow HTTP redirects for this domain during HTTP checks |
| `domains[].path` | string | `/` | HTTP request path used during HTTP checks. Query strings are supported |
| `domains[].method` | string | `GET` | HTTP request method used during HTTP checks |
| `domains[].headers` | map[string]string | `{}` | HTTP request headers used during HTTP checks |
| `domains[].expectedStatusCodes` | []int | `[]` | Exact HTTP status codes accepted as healthy. When empty, any `2xx`, `3xx`, or `4xx` response is healthy for backward compatibility |
| `checkTimeout` | duration | `30s` | Timeout for each health check |
| `checkInterval` | duration | `1m` | Interval between check cycles |
| `dialRetries` | int | `3` | Number of connection attempts for HTTP and certificate checks. HTTPS attempts include both TCP dial and TLS handshake |
| `includeIPv4` | bool | `true` | Include IPv4 addresses returned by DNS resolution |
| `includeIPv6` | bool | `true` | Include IPv6 addresses returned by DNS resolution |
| `includeCertCheck` | bool | `true` | Enable TLS certificate validation |
| `includeHTTPCheck` | bool | `true` | Enable HTTP connectivity checks |

### Environment Variables

All configuration can be overridden using environment variables with the prefix `COLLECTORS_DOMAIN_`:

| Environment Variable | Maps To | Example |
|---------------------|---------|---------|
| `COLLECTORS_DOMAIN_DOMAINS` | `domains` | `example.com,api.example.com` |
| `COLLECTORS_DOMAIN_CHECK_TIMEOUT` | `checkTimeout` | `10s` |
| `COLLECTORS_DOMAIN_CHECK_INTERVAL` | `checkInterval` | `10m` |
| `COLLECTORS_DOMAIN_DIAL_RETRIES` | `dialRetries` | `3` |
| `COLLECTORS_DOMAIN_INCLUDE_IPV4` | `includeIPv4` | `true` |
| `COLLECTORS_DOMAIN_INCLUDE_IPV6` | `includeIPv6` | `false` |
| `COLLECTORS_DOMAIN_INCLUDE_CERT_CHECK` | `includeCertCheck` | `true` |
| `COLLECTORS_DOMAIN_INCLUDE_HTTP_CHECK` | `includeHTTPCheck` | `false` |

Notes:

- `COLLECTORS_DOMAIN_DOMAINS` only supports the legacy comma-separated string format.
- If `COLLECTORS_DOMAIN_DOMAINS` is set, it overrides the YAML `domains` list.
- `ips`, `skipTLSVerify`, `followHTTPRedirects`, `path`, `method`, `headers`, and `expectedStatusCodes` are only configurable through YAML object entries.

## Metrics

### `sealos_domain_health`

**Type:** Gauge
**Labels:**
- `domain`: Domain name being monitored
- `type`: Metric type (`resolve`, `ip_count`, `healthy_ips`, `unhealthy_ips`)

**Description:** Domain-level health metrics providing an overview of the domain's resolution and IP health status.

**Metric Types:**
- `resolve`: DNS resolution or fixed-IP selection status (1=success, 0=failure)
- `ip_count`: Total number of resolved or configured IPs for the domain
- `healthy_ips`: Number of IPs that passed all enabled health checks
- `unhealthy_ips`: Number of IPs that failed one or more health checks

**Example:**
```promql
# DNS resolution successful, 2 IPs resolved, all healthy
sealos_domain_health{domain="example.com",type="resolve"} 1
sealos_domain_health{domain="example.com",type="ip_count"} 2
sealos_domain_health{domain="example.com",type="healthy_ips"} 2
sealos_domain_health{domain="example.com",type="unhealthy_ips"} 0

# DNS resolution failed
sealos_domain_health{domain="bad.example.com",type="resolve"} 0
sealos_domain_health{domain="bad.example.com",type="ip_count"} 0
sealos_domain_health{domain="bad.example.com",type="healthy_ips"} 0
sealos_domain_health{domain="bad.example.com",type="unhealthy_ips"} 0

# DNS resolution successful but no IPs returned
sealos_domain_health{domain="noip.example.com",type="resolve"} 1
sealos_domain_health{domain="noip.example.com",type="ip_count"} 0
sealos_domain_health{domain="noip.example.com",type="healthy_ips"} 0
sealos_domain_health{domain="noip.example.com",type="unhealthy_ips"} 0

# 3 IPs resolved, 1 unhealthy
sealos_domain_health{domain="partial.example.com",type="resolve"} 1
sealos_domain_health{domain="partial.example.com",type="ip_count"} 3
sealos_domain_health{domain="partial.example.com",type="healthy_ips"} 2
sealos_domain_health{domain="partial.example.com",type="unhealthy_ips"} 1
```

**Common Queries:**
```promql
# Domains with DNS resolution failures
sealos_domain_health{type="resolve"} == 0

# Domains with no IPs
sealos_domain_health{type="ip_count"} == 0

# Domains with unhealthy IPs
sealos_domain_health{type="unhealthy_ips"} > 0

# Domain health ratio (percentage of healthy IPs)
sealos_domain_health{type="healthy_ips"} / sealos_domain_health{type="ip_count"} * 100
```

### `sealos_domain_dns_status`

**Type:** Gauge
**Labels:**
- `domain`: Domain name being monitored
- `error_type`: DNS error type if resolution failed (empty if successful)

**Values:**
- `1`: DNS resolution succeeded
- `0`: DNS resolution failed

**Example:**
```promql
# DNS resolution succeeded
sealos_domain_dns_status{domain="example.com",error_type=""} 1

# DNS resolution failure
sealos_domain_dns_status{domain="bad.example.com",error_type="DNSNoSuchHost"} 0

# DNS resolved no usable addresses after filtering
sealos_domain_dns_status{domain="noip.example.com",error_type="DNSNoAnswer"} 0
```

### `sealos_domain_status`

**Type:** Gauge
**Labels:**
- `domain`: Domain name being monitored
- `ip`: Resolved IP address
- `check_type`: Type of check performed (`cert`, `http`)
- `error_type`: Error type if check failed (empty if successful)

**Values:**
- `1`: Check passed
- `0`: Check failed

Successful checks use `error_type=""`.
Checks that were not executed are not emitted.

**Example:**
```promql
# Healthy domain with successful checks
sealos_domain_status{domain="example.com",ip="93.184.216.34",check_type="http",error_type=""} 1
sealos_domain_status{domain="example.com",ip="93.184.216.34",check_type="cert",error_type=""} 1

# HTTP check failure for specific IP
sealos_domain_status{domain="slow.example.com",ip="1.2.3.4",check_type="http",error_type="Timeout"} 0
```

### `sealos_domain_cert_expiry_seconds`

**Type:** Gauge
**Labels:**
- `domain`: Domain name being monitored
- `ip`: IP address of the endpoint
- `error_type`: Error type if cert check failed

**Description:** Time in seconds until the TLS certificate expires. Negative values indicate expired certificates.

**Example:**
```promql
sealos_domain_cert_expiry_seconds{domain="example.com",ip="93.184.216.34",error_type=""} 7776000
sealos_domain_cert_expiry_seconds{domain="expired.example.com",ip="1.2.3.4",error_type=""} -86400
```

### `sealos_domain_response_time_seconds`

**Type:** Gauge
**Labels:**
- `domain`: Domain name being monitored
- `ip`: IP address of the endpoint

**Description:** Response time for the domain health check in seconds.

**Example:**
```promql
sealos_domain_response_time_seconds{domain="example.com",ip="93.184.216.34"} 0.125
```

## Health Check Logic

### IP Health Determination

An IP is considered **healthy** if:

- All enabled checks pass successfully
- If `includeHTTPCheck=true`: HTTP check must succeed
- If `includeCertCheck=true`: Certificate check must succeed

An IP is considered **unhealthy** if:

- Any enabled check fails

### TLS Verification

- TLS certificate checks are performed independently for each resolved IP.
- DNS results can be filtered globally with `includeIPv4` and `includeIPv6`.
- When `domains[].ips` is set, the collector skips DNS for that domain and checks the configured IPs. HTTP Host, request URL host, and TLS SNI still use `domains[].endpoint`.
- `includeIPv4` and `includeIPv6` filter DNS results only; fixed `ips` are checked as configured.
- HTTP checks and certificate checks both honor the per-domain `skipTLSVerify` option.
- HTTP checks use per-domain `path`, `method`, `headers`, and `expectedStatusCodes` when configured.
- When `expectedStatusCodes` is empty, HTTP checks keep the legacy behavior where any `2xx`, `3xx`, or `4xx` status is treated as healthy.
- When `domains[].followHTTPRedirects=true`, redirects to the original monitored host continue to use the resolved IP being checked. Redirects to a different host fall back to the default dial path so the redirected hostname is resolved normally.
- When `domains[].followHTTPRedirects=false`, the HTTP check returns the first redirect response instead of following it.
- When `skipTLSVerify=true`, TLS chain and hostname verification are skipped for that domain. This is intended for internal endpoints with self-signed or privately issued certificates.

### DNS Resolution Failures

When DNS resolution fails or returns no IPs:

- `sealos_domain_health{type="resolve"}` is set to `0` (for DNS failures) or `1` (for empty IP list)
- `sealos_domain_health{type="ip_count"}` is set to `0`
- `sealos_domain_dns_status` is emitted once per domain
- `http` and `cert` status metrics are not emitted because those checks were not executed
- This ensures monitoring systems are always aware of domain problems

### Example Scenarios

1. **Healthy domain**: All IPs pass all checks
   - `healthy_ips` = total IPs
   - `unhealthy_ips` = 0

2. **DNS failure**: Cannot resolve domain
   - `resolve` = 0
   - `ip_count` = 0
   - Metrics still exposed with empty IP

3. **Partial failure**: Some IPs fail checks
   - `resolve` = 1
   - `healthy_ips` < `ip_count`
   - `unhealthy_ips` > 0

4. **Empty resolution**: DNS succeeds but no IPs
   - `resolve` = 1
   - `ip_count` = 0
   - Metrics exposed with empty IP

## Collector Type

**Type:** Polling
**Leader Election Required:** No

The Domain collector runs independently on each node and polls configured domains at regular intervals.
