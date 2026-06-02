//nolint:testpackage // white-box tests intentionally exercise internal checker helpers.
package domain

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

func TestDomainCheckerUsesConfiguredIPs(t *testing.T) {
	var sawHost string

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHost = r.Host

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, portValue, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to split server address: %v", err)
	}

	port, err := strconv.Atoi(portValue)
	if err != nil {
		t.Fatalf("failed to parse server port: %v", err)
	}

	domain := monitoredDomain{
		endpoint:            "domain.example.invalid:" + portValue,
		target:              DomainTarget{Host: "domain.example.invalid", Port: port},
		ips:                 []string{"127.0.0.1"},
		skipTLSVerify:       true,
		followHTTPRedirects: true,
		httpMethod:          http.MethodGet,
		httpPath:            "/",
	}

	checker := NewDomainChecker(
		5*time.Second,
		true,
		true,
		false,
		true,
		true,
		1,
	)

	health, ipHealths := checker.CheckIPs(context.Background(), domain, log.NewEntry(log.New()))

	if !health.ResolveOk || health.IPCount != 1 || health.HealthyIPs != 1 ||
		health.UnhealthyIPs != 0 {
		t.Fatalf("unexpected domain health with configured IPs: %#v", health)
	}

	if len(ipHealths) != 1 || ipHealths[0].IP != "127.0.0.1" || !ipHealths[0].DNSOk {
		t.Fatalf("unexpected IP health with configured IPs: %#v", ipHealths)
	}

	if !ipHealths[0].HTTPOk {
		t.Fatalf("HTTP check should pass with configured IP: %#v", ipHealths[0])
	}

	if sawHost != domain.endpoint {
		t.Fatalf("request Host = %q, want %q", sawHost, domain.endpoint)
	}
}
