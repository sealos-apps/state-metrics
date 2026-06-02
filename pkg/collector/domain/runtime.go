package domain

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"
)

const defaultHTTPSPort = 443

// DomainTarget contains the parsed network target for a monitored domain.
type DomainTarget struct {
	Host string
	Port int
}

type monitoredDomain struct {
	endpoint            string
	target              DomainTarget
	ips                 []string
	skipTLSVerify       bool
	followHTTPRedirects bool
	httpMethod          string
	httpPath            string
	httpHeaders         map[string]string
	expectedStatusCodes []int
}

type monitoredDomainConfig struct {
	Endpoint            string            `mapstructure:"endpoint"`
	IPs                 []string          `mapstructure:"ips"`
	SkipTLSVerify       bool              `mapstructure:"skipTLSVerify"`
	FollowHTTPRedirects *bool             `mapstructure:"followHTTPRedirects"`
	HTTPPath            string            `mapstructure:"path"`
	HTTPMethod          string            `mapstructure:"method"`
	HTTPHeaders         map[string]string `mapstructure:"headers"`
	ExpectedStatusCodes []int             `mapstructure:"expectedStatusCodes"`
}

type runtimeConfig struct {
	domains          []monitoredDomain
	checkTimeout     time.Duration
	checkInterval    time.Duration
	dialRetries      int
	includeCertCheck bool
	includeHTTPCheck bool
	includeIPv4      bool
	includeIPv6      bool
}

func newRuntimeConfig(cfg *Config) (*runtimeConfig, error) {
	domainItems := cfg.Domains
	if len(cfg.DomainsEnv) > 0 {
		domainItems = make([]any, 0, len(cfg.DomainsEnv))
		for _, value := range cfg.DomainsEnv {
			domainItems = append(domainItems, value)
		}
	}

	domains, err := parseMonitoredDomains(domainItems)
	if err != nil {
		return nil, err
	}

	return &runtimeConfig{
		domains:          domains,
		checkTimeout:     cfg.CheckTimeout,
		checkInterval:    cfg.CheckInterval,
		dialRetries:      normalizeDialRetries(cfg.DialRetries),
		includeCertCheck: cfg.IncludeCertCheck,
		includeHTTPCheck: cfg.IncludeHTTPCheck,
		includeIPv4:      cfg.IncludeIPv4,
		includeIPv6:      cfg.IncludeIPv6,
	}, nil
}

func normalizeDialRetries(retries int) int {
	if retries < 1 {
		return 1
	}

	return retries
}

func parseMonitoredDomains(values []any) ([]monitoredDomain, error) {
	domains := make([]monitoredDomain, 0, len(values))

	for _, value := range values {
		domain, err := parseMonitoredDomain(value)
		if err != nil {
			return nil, err
		}

		if domain.endpoint == "" {
			continue
		}

		domains = append(domains, domain)
	}

	return domains, nil
}

func parseMonitoredDomain(value any) (monitoredDomain, error) {
	switch v := value.(type) {
	case string:
		return parseMonitoredDomainString(v)
	case map[string]any:
		return parseMonitoredDomainMap(v)
	default:
		return monitoredDomain{}, fmt.Errorf("invalid domain entry type %T", value)
	}
}

func parseMonitoredDomainString(value string) (monitoredDomain, error) {
	endpoint := strings.TrimSpace(value)
	if endpoint == "" {
		return monitoredDomain{}, nil
	}

	target, err := parseDomainTarget(endpoint)
	if err != nil {
		return monitoredDomain{}, err
	}

	return monitoredDomain{
		endpoint:            endpoint,
		target:              target,
		followHTTPRedirects: true,
		httpMethod:          http.MethodGet,
		httpPath:            "/",
	}, nil
}

func parseMonitoredDomainMap(value map[string]any) (monitoredDomain, error) {
	var cfg monitoredDomainConfig

	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName:          "mapstructure",
		Result:           &cfg,
		WeaklyTypedInput: true,
	})
	if err != nil {
		return monitoredDomain{}, fmt.Errorf("failed to create domain entry decoder: %w", err)
	}

	if err := decoder.Decode(value); err != nil {
		return monitoredDomain{}, fmt.Errorf("invalid domain entry %v: %w", value, err)
	}

	if strings.TrimSpace(cfg.Endpoint) == "" {
		return monitoredDomain{}, errors.New("invalid domain entry: endpoint is required")
	}

	domain, err := parseMonitoredDomainString(cfg.Endpoint)
	if err != nil {
		return monitoredDomain{}, err
	}

	if domain.endpoint == "" {
		return monitoredDomain{}, nil
	}

	ips, err := normalizeDomainIPs(cfg.IPs, cfg.Endpoint)
	if err != nil {
		return monitoredDomain{}, err
	}

	domain.ips = ips

	domain.skipTLSVerify = cfg.SkipTLSVerify
	if cfg.FollowHTTPRedirects != nil {
		domain.followHTTPRedirects = *cfg.FollowHTTPRedirects
	}

	if strings.TrimSpace(cfg.HTTPMethod) != "" {
		domain.httpMethod = strings.ToUpper(strings.TrimSpace(cfg.HTTPMethod))
	}

	if strings.TrimSpace(cfg.HTTPPath) != "" {
		domain.httpPath = strings.TrimSpace(cfg.HTTPPath)
	}

	if len(cfg.HTTPHeaders) > 0 {
		domain.httpHeaders = cfg.HTTPHeaders
	}

	if len(cfg.ExpectedStatusCodes) > 0 {
		expectedStatusCodes, err := normalizeExpectedStatusCodes(cfg.ExpectedStatusCodes)
		if err != nil {
			return monitoredDomain{}, err
		}

		domain.expectedStatusCodes = expectedStatusCodes
	}

	return domain, nil
}

func normalizeDomainIPs(values []string, endpoint string) ([]string, error) {
	ips := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))

	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}

		ip := net.ParseIP(value)
		if ip == nil {
			return nil, fmt.Errorf(
				"invalid domain entry %q: invalid ip %q",
				endpoint,
				value,
			)
		}

		normalized := ip.String()
		if _, exists := seen[normalized]; exists {
			continue
		}

		seen[normalized] = struct{}{}
		ips = append(ips, normalized)
	}

	return ips, nil
}

func normalizeExpectedStatusCodes(statusCodes []int) ([]int, error) {
	seen := make(map[int]struct{}, len(statusCodes))
	normalized := make([]int, 0, len(statusCodes))

	for _, statusCode := range statusCodes {
		if statusCode < 100 || statusCode > 599 {
			return nil, fmt.Errorf(
				"invalid expected HTTP status code %d: must be between 100 and 599",
				statusCode,
			)
		}

		if _, ok := seen[statusCode]; ok {
			continue
		}

		seen[statusCode] = struct{}{}
		normalized = append(normalized, statusCode)
	}

	return normalized, nil
}

func parseDomainTarget(value string) (DomainTarget, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return DomainTarget{}, nil
	}

	if strings.Contains(raw, "://") || strings.ContainsAny(raw, "/?#") {
		return DomainTarget{}, fmt.Errorf(
			"invalid domain endpoint %q: only host or host:port is supported",
			value,
		)
	}

	host, port, err := net.SplitHostPort(raw)
	if err == nil {
		portNum, err := parsePort(port, value)
		if err != nil {
			return DomainTarget{}, err
		}

		return newDomainTarget(host, portNum, value)
	}

	var addrErr *net.AddrError
	if errors.As(err, &addrErr) {
		switch addrErr.Err {
		case "missing port in address":
			return newDomainTarget(raw, defaultHTTPSPort, value)
		case "too many colons in address":
			if net.ParseIP(raw) != nil {
				return newDomainTarget(raw, defaultHTTPSPort, value)
			}

			return DomainTarget{}, fmt.Errorf(
				"invalid domain endpoint %q: IPv6 addresses with ports must use [host]:port",
				value,
			)
		}
	}

	return DomainTarget{}, fmt.Errorf("invalid domain endpoint %q: %w", value, err)
}

func newDomainTarget(host string, port int, original string) (DomainTarget, error) {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")

	if host == "" {
		return DomainTarget{}, fmt.Errorf("invalid domain endpoint %q: host is empty", original)
	}

	if port < 1 || port > 65535 {
		return DomainTarget{}, fmt.Errorf(
			"invalid domain endpoint %q: port %d out of range",
			original,
			port,
		)
	}

	return DomainTarget{
		Host: host,
		Port: port,
	}, nil
}

func parsePort(port, original string) (int, error) {
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return 0, fmt.Errorf("invalid domain endpoint %q: invalid port %q", original, port)
	}

	if portNum < 1 || portNum > 65535 {
		return 0, fmt.Errorf("invalid domain endpoint %q: port %d out of range", original, portNum)
	}

	return portNum, nil
}
