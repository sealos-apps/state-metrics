//nolint:testpackage // white-box tests intentionally exercise internal parsing helpers
package domain

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/labring/sealos-state-metrics/pkg/config"
	"github.com/mitchellh/mapstructure"
)

func TestNewDefaultConfig(t *testing.T) {
	cfg := NewDefaultConfig()

	if cfg.CheckTimeout != 30*time.Second {
		t.Fatalf("CheckTimeout = %v, want 30s", cfg.CheckTimeout)
	}

	if cfg.CheckInterval != time.Minute {
		t.Fatalf("CheckInterval = %v, want 1m", cfg.CheckInterval)
	}

	if cfg.DialRetries != 3 {
		t.Fatalf("DialRetries = %d, want 3", cfg.DialRetries)
	}

	if !cfg.IncludeIPv4 {
		t.Fatal("IncludeIPv4 = false, want true")
	}

	if !cfg.IncludeIPv6 {
		t.Fatal("IncludeIPv6 = false, want true")
	}
}

func TestParseDomainTarget(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantHost  string
		wantPort  int
		wantError bool
	}{
		{
			name:     "host only",
			input:    "example.com",
			wantHost: "example.com",
			wantPort: 443,
		},
		{
			name:     "host with custom port",
			input:    "registry.fake.local:5000",
			wantHost: "registry.fake.local",
			wantPort: 5000,
		},
		{
			name:     "ipv6 host with custom port",
			input:    "[::1]:5000",
			wantHost: "::1",
			wantPort: 5000,
		},
		{
			name:     "ipv6 host only with brackets",
			input:    "[::1]",
			wantHost: "::1",
			wantPort: 443,
		},
		{
			name:     "ipv6 host only without brackets",
			input:    "::1",
			wantHost: "::1",
			wantPort: 443,
		},
		{
			name:      "scheme not allowed",
			input:     "https://example.com:5000",
			wantError: true,
		},
		{
			name:      "missing host",
			input:     ":5000",
			wantError: true,
		},
		{
			name:      "missing port value",
			input:     "example.com:",
			wantError: true,
		},
		{
			name:      "invalid port",
			input:     "example.com:abc",
			wantError: true,
		},
		{
			name:      "port out of range",
			input:     "example.com:70000",
			wantError: true,
		},
		{
			name:     "ipv6 literal without brackets",
			input:    "2001:db8::1:5000",
			wantHost: "2001:db8::1:5000",
			wantPort: 443,
		},
		{
			name:      "invalid multi-colon hostname",
			input:     "host:name:5000",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := parseDomainTarget(tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error for %q", tt.input)
				}
				return
			}

			if err != nil {
				t.Fatalf("parseDomainTarget(%q) returned error: %v", tt.input, err)
			}

			if target.Host != tt.wantHost {
				t.Fatalf("target.Host = %q, want %q", target.Host, tt.wantHost)
			}

			if target.Port != tt.wantPort {
				t.Fatalf("target.Port = %d, want %d", target.Port, tt.wantPort)
			}
		})
	}
}

func TestNewRuntimeConfig(t *testing.T) {
	cfg := &Config{
		Domains: []any{
			"example.com",
			" registry.fake.local:5000 ",
			"",
			"   ",
		},
		CheckTimeout:     7 * time.Second,
		CheckInterval:    11 * time.Minute,
		DialRetries:      5,
		IncludeCertCheck: true,
		IncludeHTTPCheck: false,
		IncludeIPv4:      true,
		IncludeIPv6:      false,
	}

	runtimeCfg, err := newRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("newRuntimeConfig() returned error: %v", err)
	}

	if len(runtimeCfg.domains) != 2 {
		t.Fatalf("expected 2 runtime domains, got %d", len(runtimeCfg.domains))
	}

	first := runtimeCfg.domains[0]
	if first.endpoint != "example.com" || first.target.Host != "example.com" ||
		first.target.Port != 443 {
		t.Fatalf("unexpected first domain: %#v", first)
	}

	if first.skipTLSVerify {
		t.Fatalf("first.skipTLSVerify = true, want false")
	}

	if !first.followHTTPRedirects {
		t.Fatal("first.followHTTPRedirects = false, want true")
	}

	second := runtimeCfg.domains[1]
	if second.endpoint != "registry.fake.local:5000" ||
		second.target.Host != "registry.fake.local" ||
		second.target.Port != 5000 {
		t.Fatalf("unexpected second domain: %#v", second)
	}

	if runtimeCfg.checkTimeout != 7*time.Second {
		t.Fatalf("checkTimeout = %v, want %v", runtimeCfg.checkTimeout, 7*time.Second)
	}

	if runtimeCfg.checkInterval != 11*time.Minute {
		t.Fatalf("checkInterval = %v, want %v", runtimeCfg.checkInterval, 11*time.Minute)
	}

	if runtimeCfg.dialRetries != 5 {
		t.Fatalf("dialRetries = %d, want 5", runtimeCfg.dialRetries)
	}

	if !runtimeCfg.includeCertCheck {
		t.Fatal("includeCertCheck = false, want true")
	}

	if runtimeCfg.includeHTTPCheck {
		t.Fatal("includeHTTPCheck = true, want false")
	}

	if !runtimeCfg.includeIPv4 {
		t.Fatal("includeIPv4 = false, want true")
	}

	if runtimeCfg.includeIPv6 {
		t.Fatal("includeIPv6 = true, want false")
	}
}

func TestNewRuntimeConfig_NormalizesDialRetries(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.DialRetries = 0

	runtimeCfg, err := newRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("newRuntimeConfig() returned error: %v", err)
	}

	if runtimeCfg.dialRetries != 1 {
		t.Fatalf("dialRetries = %d, want 1", runtimeCfg.dialRetries)
	}
}

func TestNewRuntimeConfig_ObjectDomains(t *testing.T) {
	cfg := &Config{
		Domains: []any{
			map[string]any{
				"endpoint":            "example.com",
				"skipTLSVerify":       false,
				"followHTTPRedirects": false,
			},
			map[string]any{
				"endpoint":            "internal.example.local:8443",
				"ips":                 []any{" 127.0.0.1 ", "127.0.0.1", "::1"},
				"skipTLSVerify":       true,
				"followHTTPRedirects": true,
			},
		},
		CheckTimeout:     30 * time.Second,
		CheckInterval:    time.Minute,
		IncludeCertCheck: true,
		IncludeHTTPCheck: true,
		IncludeIPv4:      true,
		IncludeIPv6:      true,
	}

	runtimeCfg, err := newRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("newRuntimeConfig() returned error: %v", err)
	}

	if len(runtimeCfg.domains) != 2 {
		t.Fatalf("expected 2 runtime domains, got %d", len(runtimeCfg.domains))
	}

	first := runtimeCfg.domains[0]
	if first.endpoint != "example.com" || first.skipTLSVerify || first.followHTTPRedirects {
		t.Fatalf("unexpected first domain: %#v", first)
	}

	second := runtimeCfg.domains[1]
	if second.endpoint != "internal.example.local:8443" ||
		second.target.Host != "internal.example.local" ||
		second.target.Port != 8443 ||
		len(second.ips) != 2 ||
		second.ips[0] != "127.0.0.1" ||
		second.ips[1] != "::1" ||
		!second.skipTLSVerify ||
		!second.followHTTPRedirects {
		t.Fatalf("unexpected second domain: %#v", second)
	}
}

func TestNewRuntimeConfig_MixedDomains(t *testing.T) {
	cfg := &Config{
		Domains: []any{
			"example.com",
			map[string]any{
				"endpoint":            "internal.example.local:8443",
				"skipTLSVerify":       true,
				"followHTTPRedirects": false,
			},
			"api.example.com",
		},
		CheckTimeout:     30 * time.Second,
		CheckInterval:    time.Minute,
		IncludeCertCheck: true,
		IncludeHTTPCheck: true,
		IncludeIPv4:      true,
		IncludeIPv6:      true,
	}

	runtimeCfg, err := newRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("newRuntimeConfig() returned error: %v", err)
	}

	if len(runtimeCfg.domains) != 3 {
		t.Fatalf("expected 3 runtime domains, got %d", len(runtimeCfg.domains))
	}

	if runtimeCfg.domains[0].endpoint != "example.com" || runtimeCfg.domains[0].skipTLSVerify {
		t.Fatalf("unexpected first mixed domain: %#v", runtimeCfg.domains[0])
	}

	if !runtimeCfg.domains[0].followHTTPRedirects {
		t.Fatalf("unexpected first mixed domain redirect setting: %#v", runtimeCfg.domains[0])
	}

	if runtimeCfg.domains[1].endpoint != "internal.example.local:8443" ||
		runtimeCfg.domains[1].target.Port != 8443 ||
		!runtimeCfg.domains[1].skipTLSVerify ||
		runtimeCfg.domains[1].followHTTPRedirects {
		t.Fatalf("unexpected second mixed domain: %#v", runtimeCfg.domains[1])
	}

	if runtimeCfg.domains[2].endpoint != "api.example.com" || runtimeCfg.domains[2].skipTLSVerify ||
		!runtimeCfg.domains[2].followHTTPRedirects {
		t.Fatalf("unexpected third mixed domain: %#v", runtimeCfg.domains[2])
	}
}

func TestParseMonitoredDomainMap_WeaklyTypedInput(t *testing.T) {
	domain, err := parseMonitoredDomainMap(map[string]any{
		"endpoint":            "internal.example.local:8443",
		"skipTLSVerify":       "true",
		"followHTTPRedirects": "false",
	})
	if err != nil {
		t.Fatalf("parseMonitoredDomainMap() returned error: %v", err)
	}

	if domain.endpoint != "internal.example.local:8443" {
		t.Fatalf("domain.endpoint = %q, want internal.example.local:8443", domain.endpoint)
	}

	if domain.target.Host != "internal.example.local" || domain.target.Port != 8443 {
		t.Fatalf("unexpected target: %#v", domain.target)
	}

	if !domain.skipTLSVerify {
		t.Fatal("domain.skipTLSVerify = false, want true")
	}

	if domain.followHTTPRedirects {
		t.Fatal("domain.followHTTPRedirects = true, want false")
	}
}

func TestParseMonitoredDomainMap_HTTPCheckOptions(t *testing.T) {
	domain, err := parseMonitoredDomainMap(map[string]any{
		"endpoint":            "internal.example.local:8443",
		"path":                "healthz?ready=true",
		"method":              "post",
		"headers":             map[string]any{"X-Probe": "domain", "X-Trace": 1234},
		"expectedStatusCodes": []any{http.StatusOK, "201", http.StatusOK},
	})
	if err != nil {
		t.Fatalf("parseMonitoredDomainMap() returned error: %v", err)
	}

	if domain.httpPath != "healthz?ready=true" {
		t.Fatalf("domain.httpPath = %q, want healthz?ready=true", domain.httpPath)
	}

	if domain.httpMethod != http.MethodPost {
		t.Fatalf("domain.httpMethod = %q, want %q", domain.httpMethod, http.MethodPost)
	}

	wantHeaders := map[string]string{
		"X-Probe": "domain",
		"X-Trace": "1234",
	}
	if !reflect.DeepEqual(domain.httpHeaders, wantHeaders) {
		t.Fatalf("domain.httpHeaders = %#v, want %#v", domain.httpHeaders, wantHeaders)
	}

	wantStatusCodes := []int{http.StatusOK, http.StatusCreated}
	if !reflect.DeepEqual(domain.expectedStatusCodes, wantStatusCodes) {
		t.Fatalf(
			"domain.expectedStatusCodes = %#v, want %#v",
			domain.expectedStatusCodes,
			wantStatusCodes,
		)
	}
}

func TestNewRuntimeConfig_FromYAMLHTTPCheckOptions(t *testing.T) {
	yamlContent := []byte(`
collectors:
  domain:
    domains:
      - endpoint: internal.example.local:8443
        skipTLSVerify: true
        followHTTPRedirects: false
        path: /healthz?ready=true
        method: post
        headers:
          X-Probe: sealos-state-metrics
          X-Retry: 1
        expectedStatusCodes:
          - 200
          - 204
          - 200
    checkTimeout: 5s
    checkInterval: 2m
    dialRetries: 4
    includeIPv4: true
    includeIPv6: false
    includeCertCheck: false
    includeHTTPCheck: true
`)

	cfg := NewDefaultConfig()

	loader := config.NewModuleConfigLoader(
		yamlContent,
		config.WithModuleDecodeHook(mapstructure.StringToTimeDurationHookFunc()),
	)
	if err := loader.LoadModuleConfig("collectors.domain", cfg); err != nil {
		t.Fatalf("LoadModuleConfig() returned error: %v", err)
	}

	runtimeCfg, err := newRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("newRuntimeConfig() returned error: %v", err)
	}

	if runtimeCfg.checkTimeout != 5*time.Second {
		t.Fatalf("checkTimeout = %v, want 5s", runtimeCfg.checkTimeout)
	}

	if runtimeCfg.checkInterval != 2*time.Minute {
		t.Fatalf("checkInterval = %v, want 2m", runtimeCfg.checkInterval)
	}

	if runtimeCfg.dialRetries != 4 {
		t.Fatalf("dialRetries = %d, want 4", runtimeCfg.dialRetries)
	}

	if runtimeCfg.includeCertCheck {
		t.Fatal("includeCertCheck = true, want false")
	}

	if !runtimeCfg.includeHTTPCheck {
		t.Fatal("includeHTTPCheck = false, want true")
	}

	if len(runtimeCfg.domains) != 1 {
		t.Fatalf("expected 1 domain, got %d", len(runtimeCfg.domains))
	}

	domain := runtimeCfg.domains[0]
	if domain.endpoint != "internal.example.local:8443" ||
		domain.target.Host != "internal.example.local" ||
		domain.target.Port != 8443 ||
		!domain.skipTLSVerify ||
		domain.followHTTPRedirects {
		t.Fatalf("unexpected runtime domain: %#v", domain)
	}

	if domain.httpPath != "/healthz?ready=true" {
		t.Fatalf("domain.httpPath = %q, want /healthz?ready=true", domain.httpPath)
	}

	if domain.httpMethod != http.MethodPost {
		t.Fatalf("domain.httpMethod = %q, want %q", domain.httpMethod, http.MethodPost)
	}

	wantHeaders := map[string]string{
		"X-Probe": "sealos-state-metrics",
		"X-Retry": "1",
	}
	if !reflect.DeepEqual(domain.httpHeaders, wantHeaders) {
		t.Fatalf("domain.httpHeaders = %#v, want %#v", domain.httpHeaders, wantHeaders)
	}

	wantStatusCodes := []int{http.StatusOK, http.StatusNoContent}
	if !reflect.DeepEqual(domain.expectedStatusCodes, wantStatusCodes) {
		t.Fatalf(
			"domain.expectedStatusCodes = %#v, want %#v",
			domain.expectedStatusCodes,
			wantStatusCodes,
		)
	}
}

func TestParseMonitoredDomainMap_InvalidExpectedStatusCode(t *testing.T) {
	_, err := parseMonitoredDomainMap(map[string]any{
		"endpoint":            "internal.example.local:8443",
		"expectedStatusCodes": []any{99},
	})
	if err == nil {
		t.Fatal("expected error for invalid expected status code")
	}
}

func TestParseMonitoredDomainMap_InvalidIP(t *testing.T) {
	_, err := parseMonitoredDomainMap(map[string]any{
		"endpoint": "internal.example.local:8443",
		"ips":      []any{"10.0.0.1", "not-an-ip"},
	})
	if err == nil {
		t.Fatal("expected error for invalid configured IP")
	}
}

func TestParseMonitoredDomainMap_MissingEndpoint(t *testing.T) {
	_, err := parseMonitoredDomainMap(map[string]any{
		"skipTLSVerify": true,
	})
	if err == nil {
		t.Fatal("expected error for missing endpoint")
	}
}

func TestNewRuntimeConfig_DomainsEnvOverridesYAML(t *testing.T) {
	cfg := &Config{
		Domains: []any{
			map[string]any{
				"endpoint":      "yaml.example.com",
				"skipTLSVerify": true,
			},
		},
		DomainsEnv:       []string{"env.example.com", "env-alt.example.com:8443"},
		CheckTimeout:     7 * time.Second,
		CheckInterval:    11 * time.Minute,
		IncludeCertCheck: true,
		IncludeHTTPCheck: true,
		IncludeIPv4:      true,
		IncludeIPv6:      true,
	}

	runtimeCfg, err := newRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("newRuntimeConfig() returned error: %v", err)
	}

	if len(runtimeCfg.domains) != 2 {
		t.Fatalf("expected 2 runtime domains, got %d", len(runtimeCfg.domains))
	}

	if runtimeCfg.domains[0].endpoint != "env.example.com" || runtimeCfg.domains[0].skipTLSVerify {
		t.Fatalf("unexpected first env domain: %#v", runtimeCfg.domains[0])
	}

	if !runtimeCfg.domains[0].followHTTPRedirects {
		t.Fatalf("unexpected first env domain redirect setting: %#v", runtimeCfg.domains[0])
	}

	if runtimeCfg.domains[1].endpoint != "env-alt.example.com:8443" ||
		runtimeCfg.domains[1].target.Port != 8443 ||
		runtimeCfg.domains[1].skipTLSVerify ||
		!runtimeCfg.domains[1].followHTTPRedirects {
		t.Fatalf("unexpected second env domain: %#v", runtimeCfg.domains[1])
	}
}
