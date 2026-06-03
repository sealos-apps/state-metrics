package registryproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const maxConcurrentRegistryIPChecks = 10

type ErrorType string

const (
	ErrorTypeDNS        ErrorType = "DNS"
	ErrorTypeNoIP       ErrorType = "NoIP"
	ErrorTypeTimeout    ErrorType = "Timeout"
	ErrorTypeConnect    ErrorType = "Connect"
	ErrorTypeHTTPStatus ErrorType = "HTTPStatus"
	ErrorTypeRequest    ErrorType = "Request"
	ErrorTypeContext    ErrorType = "Context"
)

// RegistryHealth represents the aggregate health status of a registry proxy.
type RegistryHealth struct {
	Endpoint     string
	Info         string
	Scheme       string
	Host         string
	Port         int
	Repository   string
	Reference    string
	ResolveOk    bool
	IPCount      int
	HealthyIPs   int
	UnhealthyIPs int
	LastChecked  time.Time
}

// IPHealth represents registry proxy checks for a specific resolved IP.
type IPHealth struct {
	Endpoint   string
	Info       string
	Scheme     string
	Host       string
	Port       int
	Repository string
	Reference  string
	IP         string

	DNSChecked   bool
	DNSOk        bool
	DNSError     string
	DNSErrorType ErrorType

	APIChecked        bool
	APIOk             bool
	APIStatusCode     int
	APIError          string
	APIErrorType      ErrorType
	APIResponseTime   time.Duration
	ManifestChecked   bool
	ManifestOk        bool
	ManifestStatus    int
	ManifestError     string
	ManifestErrorType ErrorType
	ManifestTime      time.Duration

	LastChecked time.Time
}

type checkResult struct {
	Success      bool
	ResponseTime time.Duration
	StatusCode   int
	Error        string
	ErrorType    ErrorType
}

// RegistryChecker performs Docker Registry HTTP API checks.
type RegistryChecker struct {
	timeout   time.Duration
	tlsConfig *tls.Config
}

// NewRegistryChecker creates a new registry checker.
func NewRegistryChecker(timeout time.Duration) *RegistryChecker {
	return &RegistryChecker{
		timeout: timeout,
	}
}

// CheckIPs checks the registry proxy API and manifest endpoint for each resolved IP.
func (rc *RegistryChecker) CheckIPs(
	ctx context.Context,
	registry monitoredRegistry,
	logger *log.Entry,
) (*RegistryHealth, []*IPHealth) {
	now := time.Now()
	registryHealth := &RegistryHealth{
		Endpoint:    registry.endpoint,
		Info:        registry.info,
		Scheme:      registry.target.Scheme,
		Host:        registry.target.Host,
		Port:        registry.target.Port,
		Repository:  registry.repository,
		Reference:   registry.reference,
		ResolveOk:   true,
		LastChecked: now,
	}

	ips := registry.ips
	if len(ips) == 0 {
		resolvedIPs, dnsErr := resolveRegistryIPs(ctx, registry.target.Host, rc.timeout)
		if dnsErr != "" {
			logger.WithFields(log.Fields{
				"endpoint": registry.endpoint,
				"host":     registry.target.Host,
				"port":     registry.target.Port,
				"error":    dnsErr,
			}).Warn("Registry proxy DNS resolution failed")

			registryHealth.ResolveOk = false

			return registryHealth, []*IPHealth{
				newRegistryIPHealth(registry, "", now, false, dnsErr, ErrorTypeDNS),
			}
		}

		ips = resolvedIPs
	}

	if len(ips) == 0 {
		registryHealth.IPCount = 0

		return registryHealth, []*IPHealth{
			newRegistryIPHealth(
				registry,
				"",
				now,
				false,
				"DNS resolution returned no IPs",
				ErrorTypeNoIP,
			),
		}
	}

	results := make([]*IPHealth, len(ips))
	for i, ip := range ips {
		results[i] = newRegistryIPHealth(registry, ip, now, true, "", "")
	}

	workerCount := min(len(ips), maxConcurrentRegistryIPChecks)
	if workerCount <= 1 {
		for i, ip := range ips {
			rc.runIPChecks(ctx, registry, ip, results[i])
		}
	} else {
		indexCh := make(chan int)

		var wg sync.WaitGroup
		for range workerCount {
			wg.Go(func() {
				for i := range indexCh {
					rc.runIPChecks(ctx, registry, ips[i], results[i])
				}
			})
		}

	enqueue:
		for i := range ips {
			select {
			case <-ctx.Done():
				break enqueue
			case indexCh <- i:
			}
		}

		close(indexCh)
		wg.Wait()
	}

	registryHealth.IPCount = len(ips)
	for _, result := range results {
		if result.APIOk && result.ManifestOk {
			registryHealth.HealthyIPs++
		}
	}

	registryHealth.UnhealthyIPs = registryHealth.IPCount - registryHealth.HealthyIPs

	logFields := log.Fields{
		"endpoint":     registry.endpoint,
		"info":         registry.info,
		"resolvedIPs":  registryHealth.IPCount,
		"healthyIPs":   registryHealth.HealthyIPs,
		"unhealthyIPs": registryHealth.UnhealthyIPs,
		"repository":   registry.repository,
		"reference":    registry.reference,
	}
	if registryHealth.UnhealthyIPs > 0 {
		logger.WithFields(logFields).Warn("Registry proxy check completed with unhealthy IPs")
	} else {
		logger.WithFields(logFields).Debug("Registry proxy check completed")
	}

	return registryHealth, results
}

func (rc *RegistryChecker) runIPChecks(
	ctx context.Context,
	registry monitoredRegistry,
	ip string,
	health *IPHealth,
) {
	health.APIChecked = true
	apiResult := rc.checkRegistryEndpoint(ctx, registry, ip, registryAPIURL(registry), false)
	health.APIOk = apiResult.Success
	health.APIStatusCode = apiResult.StatusCode
	health.APIError = apiResult.Error
	health.APIErrorType = apiResult.ErrorType
	health.APIResponseTime = apiResult.ResponseTime

	health.ManifestChecked = true
	manifestResult := rc.checkRegistryEndpoint(
		ctx,
		registry,
		ip,
		registryManifestURL(registry),
		true,
	)
	health.ManifestOk = manifestResult.Success
	health.ManifestStatus = manifestResult.StatusCode
	health.ManifestError = manifestResult.Error
	health.ManifestErrorType = manifestResult.ErrorType
	health.ManifestTime = manifestResult.ResponseTime
}

func (rc *RegistryChecker) checkRegistryEndpoint(
	ctx context.Context,
	registry monitoredRegistry,
	ip string,
	requestURL string,
	manifest bool,
) checkResult {
	transport := newRegistryTransport(registry, ip, rc.timeout, rc.tlsConfig)
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Timeout:   rc.timeout,
		Transport: transport,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return checkResult{
			Success:   false,
			Error:     fmt.Sprintf("failed to create request: %v", err),
			ErrorType: ErrorTypeRequest,
		}
	}

	req.Host = registryAuthority(registry.target.Host, registry.target.Port)
	for name, value := range registry.headers {
		req.Header.Set(name, value)
	}

	if manifest {
		req.Header.Set("Accept", registry.manifestAcceptHeader)
	}

	start := time.Now()
	resp, err := client.Do(req)

	responseTime := time.Since(start)
	if err != nil {
		return checkResult{
			Success:      false,
			ResponseTime: responseTime,
			Error:        fmt.Sprintf("request failed: %v", err),
			ErrorType:    classifyRegistryError(err),
		}
	}

	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))

	if manifest {
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return checkResult{
				Success:      false,
				ResponseTime: responseTime,
				StatusCode:   resp.StatusCode,
				Error:        fmt.Sprintf("unexpected manifest status code: %d", resp.StatusCode),
				ErrorType:    ErrorTypeHTTPStatus,
			}
		}
	} else if !isRegistryAPIStatus(resp.StatusCode) {
		return checkResult{
			Success:      false,
			ResponseTime: responseTime,
			StatusCode:   resp.StatusCode,
			Error:        fmt.Sprintf("unexpected API status code: %d", resp.StatusCode),
			ErrorType:    ErrorTypeHTTPStatus,
		}
	}

	return checkResult{
		Success:      true,
		ResponseTime: responseTime,
		StatusCode:   resp.StatusCode,
	}
}

func isRegistryAPIStatus(statusCode int) bool {
	return (statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices) ||
		statusCode == http.StatusUnauthorized
}

func newRegistryTransport(
	registry monitoredRegistry,
	ip string,
	timeout time.Duration,
	tlsConfig *tls.Config,
) *http.Transport {
	dialer := &net.Dialer{
		Timeout: timeout,
	}
	tlsConfig = registryTLSConfig(registry, tlsConfig)

	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			targetAddr, err := registryDialAddress(registry, ip, addr)
			if err != nil {
				return nil, err
			}

			return dialer.DialContext(ctx, network, targetAddr)
		},
		TLSClientConfig: tlsConfig,
	}
}

func registryTLSConfig(registry monitoredRegistry, base *tls.Config) *tls.Config {
	tlsConfig := &tls.Config{}
	if base != nil {
		tlsConfig = base.Clone()
	}

	tlsConfig.ServerName = registry.target.Host
	if tlsConfig.MinVersion == 0 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}

	return tlsConfig
}

func registryDialAddress(registry monitoredRegistry, ip, addr string) (string, error) {
	dialHost, dialPort, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}

	if sameRegistryHost(dialHost, registry.target.Host) {
		return net.JoinHostPort(ip, dialPort), nil
	}

	return addr, nil
}

func resolveRegistryIPs(
	ctx context.Context,
	host string,
	timeout time.Duration,
) ([]string, string) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, ""
	}

	resolver := &net.Resolver{}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ips, err := resolver.LookupHost(ctx, host)
	if err != nil {
		return nil, fmt.Sprintf("DNS lookup failed: %v", err)
	}

	return ips, ""
}

func newRegistryIPHealth(
	registry monitoredRegistry,
	ip string,
	now time.Time,
	dnsOk bool,
	dnsError string,
	dnsErrorType ErrorType,
) *IPHealth {
	return &IPHealth{
		Endpoint:     registry.endpoint,
		Info:         registry.info,
		Scheme:       registry.target.Scheme,
		Host:         registry.target.Host,
		Port:         registry.target.Port,
		Repository:   registry.repository,
		Reference:    registry.reference,
		IP:           ip,
		DNSChecked:   true,
		DNSOk:        dnsOk,
		DNSError:     dnsError,
		DNSErrorType: dnsErrorType,
		LastChecked:  now,
	}
}

func classifyRegistryError(err error) ErrorType {
	if err == nil {
		return ""
	}

	if errorsIsContext(err) {
		return ErrorTypeContext
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrorTypeTimeout
	}

	message := strings.ToLower(err.Error())
	if strings.Contains(message, "timeout") || strings.Contains(message, "deadline exceeded") {
		return ErrorTypeTimeout
	}

	if strings.Contains(message, "connection refused") ||
		strings.Contains(message, "no route to host") ||
		strings.Contains(message, "network is unreachable") {
		return ErrorTypeConnect
	}

	return ErrorTypeRequest
}

func sameRegistryHost(left, right string) bool {
	left = strings.Trim(left, "[]")
	right = strings.Trim(right, "[]")

	return strings.EqualFold(left, right)
}

func errorsIsContext(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
