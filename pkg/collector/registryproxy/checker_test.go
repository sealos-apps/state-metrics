//nolint:testpackage // white-box tests intentionally exercise internal checker helpers
package registryproxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestRegistryCheckerCheckIPsSuccess(t *testing.T) {
	var sawAPI bool

	var sawManifest bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			sawAPI = true

			w.WriteHeader(http.StatusOK)
		case "/v2/library/busybox/manifests/latest":
			sawManifest = true

			if r.Header.Get("Accept") != defaultManifestMediaType {
				t.Fatalf(
					"Accept header = %q, want %q",
					r.Header.Get("Accept"),
					defaultManifestMediaType,
				)
			}

			w.Header().Set("Content-Type", defaultManifestMediaType)

			_, _ = w.Write([]byte(`{"schemaVersion":2}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	registry := mustLocalRegistry(t, server)
	registry.info = "local test"

	checker := NewRegistryChecker(5 * time.Second)
	health, ipHealths := checker.CheckIPs(context.Background(), registry, log.NewEntry(log.New()))

	if !health.ResolveOk || health.IPCount != 1 || health.HealthyIPs != 1 ||
		health.UnhealthyIPs != 0 {
		t.Fatalf("unexpected registry health: %#v", health)
	}

	if len(ipHealths) != 1 {
		t.Fatalf("expected 1 IP health, got %d", len(ipHealths))
	}

	ipHealth := ipHealths[0]
	if !ipHealth.DNSOk || ipHealth.IP != "127.0.0.1" {
		t.Fatalf("unexpected DNS health: %#v", ipHealth)
	}

	if !ipHealth.APIOk || ipHealth.APIStatusCode != http.StatusOK {
		t.Fatalf("unexpected API health: %#v", ipHealth)
	}

	if !ipHealth.ManifestOk || ipHealth.ManifestStatus != http.StatusOK {
		t.Fatalf("unexpected manifest health: %#v", ipHealth)
	}

	if !sawAPI || !sawManifest {
		t.Fatalf("expected API and manifest endpoints to be called")
	}
}

func TestRegistryCheckerManifestFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/v2/library/busybox/manifests/latest":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	registry := mustLocalRegistry(t, server)
	checker := NewRegistryChecker(5 * time.Second)
	health, ipHealths := checker.CheckIPs(context.Background(), registry, log.NewEntry(log.New()))

	if health.ResolveOk != true || health.IPCount != 1 || health.HealthyIPs != 0 ||
		health.UnhealthyIPs != 1 {
		t.Fatalf("unexpected registry health: %#v", health)
	}

	if len(ipHealths) != 1 {
		t.Fatalf("expected 1 IP health, got %d", len(ipHealths))
	}

	ipHealth := ipHealths[0]
	if !ipHealth.APIOk {
		t.Fatalf("API check should pass: %#v", ipHealth)
	}

	if ipHealth.ManifestOk || ipHealth.ManifestStatus != http.StatusNotFound ||
		ipHealth.ManifestErrorType != ErrorTypeHTTPStatus {
		t.Fatalf("unexpected manifest health: %#v", ipHealth)
	}
}

func TestRegistryCheckerAPIUnauthorizedIsReachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusUnauthorized)
		case "/v2/library/busybox/manifests/latest":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	registry := mustLocalRegistry(t, server)
	checker := NewRegistryChecker(5 * time.Second)
	health, ipHealths := checker.CheckIPs(context.Background(), registry, log.NewEntry(log.New()))

	if health.HealthyIPs != 1 || health.UnhealthyIPs != 0 {
		t.Fatalf("unexpected registry health: %#v", health)
	}

	if len(ipHealths) != 1 {
		t.Fatalf("expected 1 IP health, got %d", len(ipHealths))
	}

	if !ipHealths[0].APIOk || ipHealths[0].APIStatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized API response should be reachable: %#v", ipHealths[0])
	}
}

func TestRegistryCheckerUsesConfiguredIPs(t *testing.T) {
	var sawHost string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHost = r.Host

		switch r.URL.Path {
		case "/v2/", "/v2/library/busybox/manifests/latest":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	registry := mustLocalRegistry(t, server)
	registry.target.Host = "registry.example.invalid"
	registry.endpoint = "http://registry.example.invalid:" + strconv.Itoa(registry.target.Port)
	registry.ips = []string{"127.0.0.1"}

	checker := NewRegistryChecker(5 * time.Second)
	health, ipHealths := checker.CheckIPs(context.Background(), registry, log.NewEntry(log.New()))

	if !health.ResolveOk || health.IPCount != 1 || health.HealthyIPs != 1 ||
		health.UnhealthyIPs != 0 {
		t.Fatalf("unexpected registry health with configured IPs: %#v", health)
	}

	if len(ipHealths) != 1 || ipHealths[0].IP != "127.0.0.1" || !ipHealths[0].DNSOk {
		t.Fatalf("unexpected IP health with configured IPs: %#v", ipHealths)
	}

	wantHost := "registry.example.invalid:" + strconv.Itoa(registry.target.Port)
	if sawHost != wantHost {
		t.Fatalf("request Host = %q, want %q", sawHost, wantHost)
	}
}

func TestRegistryCheckerUsesConfiguredIPsForHTTPS(t *testing.T) {
	var sawHost string

	var sawServerName string

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHost = r.Host
		if r.TLS != nil {
			sawServerName = r.TLS.ServerName
		}

		switch r.URL.Path {
		case "/v2/", "/v2/library/busybox/manifests/latest":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	host, portValue, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to split server address: %v", err)
	}

	port, err := strconv.Atoi(portValue)
	if err != nil {
		t.Fatalf("failed to parse server port: %v", err)
	}

	registry := monitoredRegistry{
		endpoint:             "https://example.com:" + portValue,
		target:               registryTarget{Scheme: "https", Host: "example.com", Port: port},
		ips:                  []string{host},
		repository:           "library/busybox",
		reference:            "latest",
		manifestAcceptHeader: defaultManifestMediaType,
		headers:              map[string]string{},
	}

	checker := NewRegistryChecker(5 * time.Second)

	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf(
			"server client transport has type %T, want *http.Transport",
			server.Client().Transport,
		)
	}

	checker.tlsConfig = transport.TLSClientConfig

	health, ipHealths := checker.CheckIPs(context.Background(), registry, log.NewEntry(log.New()))

	if !health.ResolveOk || health.IPCount != 1 || health.HealthyIPs != 1 ||
		health.UnhealthyIPs != 0 {
		t.Fatalf("unexpected registry health with configured HTTPS IPs: %#v", health)
	}

	if len(ipHealths) != 1 || ipHealths[0].IP != host || !ipHealths[0].DNSOk {
		t.Fatalf("unexpected IP health with configured HTTPS IPs: %#v", ipHealths)
	}

	wantHost := "example.com:" + portValue
	if sawHost != wantHost {
		t.Fatalf("request Host = %q, want %q", sawHost, wantHost)
	}

	if sawServerName != "example.com" {
		t.Fatalf("TLS ServerName = %q, want example.com", sawServerName)
	}
}

func mustLocalRegistry(t *testing.T, server *httptest.Server) monitoredRegistry {
	t.Helper()

	host, portValue, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to split server address: %v", err)
	}

	port, err := strconv.Atoi(portValue)
	if err != nil {
		t.Fatalf("failed to parse server port: %v", err)
	}

	return monitoredRegistry{
		endpoint:             "http://" + net.JoinHostPort(host, portValue),
		target:               registryTarget{Scheme: "http", Host: host, Port: port},
		repository:           "library/busybox",
		reference:            "latest",
		manifestAcceptHeader: defaultManifestMediaType,
		headers:              map[string]string{},
	}
}
