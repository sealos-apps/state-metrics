//nolint:testpackage // white-box tests intentionally exercise internal parsing helpers
package registryproxy

import (
	"testing"
	"time"
)

func TestNewDefaultConfig(t *testing.T) {
	cfg := NewDefaultConfig()

	if cfg.CheckTimeout != 30*time.Second {
		t.Fatalf("CheckTimeout = %v, want 30s", cfg.CheckTimeout)
	}

	if cfg.CheckInterval != time.Minute {
		t.Fatalf("CheckInterval = %v, want 1m", cfg.CheckInterval)
	}
}

func TestParseRegistryTarget(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   string
		wantScheme string
		wantHost   string
		wantPort   int
		wantError  bool
	}{
		{
			name:       "http URL endpoint",
			endpoint:   "http://registry.example.com:5000",
			wantScheme: "http",
			wantHost:   "registry.example.com",
			wantPort:   5000,
		},
		{
			name:       "url endpoint",
			endpoint:   "https://registry.example.com:5443",
			wantScheme: "https",
			wantHost:   "registry.example.com",
			wantPort:   5443,
		},
		{
			name:       "ipv4 literal with port",
			endpoint:   "http://67.21.84.122:5000",
			wantScheme: "http",
			wantHost:   "67.21.84.122",
			wantPort:   5000,
		},
		{
			name:       "ipv6 with port",
			endpoint:   "http://[::1]:5000",
			wantScheme: "http",
			wantHost:   "::1",
			wantPort:   5000,
		},
		{
			name:      "missing scheme",
			endpoint:  "registry.example.com:5000",
			wantError: true,
		},
		{
			name:      "missing port",
			endpoint:  "http://registry.example.com",
			wantError: true,
		},
		{
			name:      "path not allowed",
			endpoint:  "http://registry.example.com:5000/v2/",
			wantError: true,
		},
		{
			name:      "invalid port",
			endpoint:  "http://registry.example.com:bad",
			wantError: true,
		},
		{
			name:      "missing host",
			endpoint:  "http://:5000",
			wantError: true,
		},
		{
			name:      "multi-colon hostname",
			endpoint:  "http://host:name:5000",
			wantError: true,
		},
		{
			name:      "unsupported scheme",
			endpoint:  "ftp://registry.example.com:5000",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := parseRegistryTarget(tt.endpoint)
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error for %q", tt.endpoint)
				}
				return
			}

			if err != nil {
				t.Fatalf("parseRegistryTarget(%q) returned error: %v", tt.endpoint, err)
			}

			if target.Scheme != tt.wantScheme {
				t.Fatalf("target.Scheme = %q, want %q", target.Scheme, tt.wantScheme)
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
		Registries: []RegistryConfig{
			{
				Endpoint:             "http://registry.example.com:5000",
				Info:                 "internal proxy",
				IPs:                  []string{" 127.0.0.1 ", "127.0.0.1", "::1"},
				Repository:           "/library/alpine/",
				Reference:            "3.20",
				ManifestAcceptHeader: "application/vnd.oci.image.manifest.v1+json",
				Headers: map[string]string{
					"X-Probe": "sealos-state-metrics",
				},
			},
			{
				Endpoint:   "http://127.0.0.1:5000",
				Repository: "library/busybox",
				Reference:  "latest",
			},
		},
		CheckTimeout:  7 * time.Second,
		CheckInterval: 11 * time.Minute,
	}

	runtimeCfg, err := newRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("newRuntimeConfig() returned error: %v", err)
	}

	if len(runtimeCfg.registries) != 2 {
		t.Fatalf("expected 2 registries, got %d", len(runtimeCfg.registries))
	}

	first := runtimeCfg.registries[0]
	if first.endpoint != "http://registry.example.com:5000" ||
		first.info != "internal proxy" ||
		first.target.Scheme != "http" ||
		first.target.Host != "registry.example.com" ||
		first.target.Port != 5000 ||
		len(first.ips) != 2 ||
		first.ips[0] != "127.0.0.1" ||
		first.ips[1] != "::1" ||
		first.repository != "library/alpine" ||
		first.reference != "3.20" ||
		first.manifestAcceptHeader != "application/vnd.oci.image.manifest.v1+json" ||
		first.headers["X-Probe"] != "sealos-state-metrics" {
		t.Fatalf("unexpected first registry: %#v", first)
	}

	second := runtimeCfg.registries[1]
	if second.repository != "library/busybox" ||
		second.reference != "latest" ||
		second.manifestAcceptHeader != defaultManifestMediaType {
		t.Fatalf("unexpected second registry defaults: %#v", second)
	}

	if runtimeCfg.checkTimeout != 7*time.Second {
		t.Fatalf("checkTimeout = %v, want 7s", runtimeCfg.checkTimeout)
	}

	if runtimeCfg.checkInterval != 11*time.Minute {
		t.Fatalf("checkInterval = %v, want 11m", runtimeCfg.checkInterval)
	}
}

func TestRegistryURLs(t *testing.T) {
	registry, err := parseMonitoredRegistry(RegistryConfig{
		Endpoint:   "http://registry.example.com:5000",
		Repository: "/library/busybox/",
		Reference:  "sha256:abc123",
	})
	if err != nil {
		t.Fatalf("parseMonitoredRegistry returned error: %v", err)
	}

	if got := registryAPIURL(registry); got != "http://registry.example.com:5000/v2/" {
		t.Fatalf("registryAPIURL() = %q", got)
	}

	wantManifest := "http://registry.example.com:5000/v2/library/busybox/manifests/sha256:abc123"
	if got := registryManifestURL(registry); got != wantManifest {
		t.Fatalf("registryManifestURL() = %q, want %q", got, wantManifest)
	}
}

func TestParseMonitoredRegistryRequiresRepositoryAndReference(t *testing.T) {
	tests := []struct {
		name string
		cfg  RegistryConfig
	}{
		{
			name: "missing repository",
			cfg: RegistryConfig{
				Endpoint:  "http://registry.example.com:5000",
				Reference: "latest",
			},
		},
		{
			name: "missing reference",
			cfg: RegistryConfig{
				Endpoint:   "http://registry.example.com:5000",
				Repository: "library/busybox",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseMonitoredRegistry(tt.cfg); err == nil {
				t.Fatalf("expected error for %#v", tt.cfg)
			}
		})
	}
}

func TestParseMonitoredRegistryRejectsInvalidIP(t *testing.T) {
	if _, err := parseMonitoredRegistry(RegistryConfig{
		Endpoint:   "http://registry.example.com:5000",
		IPs:        []string{"10.0.0.1", "not-an-ip"},
		Repository: "library/busybox",
		Reference:  "latest",
	}); err == nil {
		t.Fatalf("expected error for invalid configured IP")
	}
}
