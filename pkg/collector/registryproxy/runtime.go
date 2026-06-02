package registryproxy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultManifestMediaType = "application/vnd.docker.distribution.manifest.v2+json"
)

type registryTarget struct {
	Scheme string
	Host   string
	Port   int
}

type monitoredRegistry struct {
	endpoint             string
	info                 string
	target               registryTarget
	ips                  []string
	repository           string
	reference            string
	manifestAcceptHeader string
	headers              map[string]string
}

type runtimeConfig struct {
	registries    []monitoredRegistry
	checkTimeout  time.Duration
	checkInterval time.Duration
}

func newRuntimeConfig(cfg *Config) (*runtimeConfig, error) {
	registries, err := parseMonitoredRegistries(cfg.Registries)
	if err != nil {
		return nil, err
	}

	return &runtimeConfig{
		registries:    registries,
		checkTimeout:  cfg.CheckTimeout,
		checkInterval: cfg.CheckInterval,
	}, nil
}

func parseMonitoredRegistries(values []RegistryConfig) ([]monitoredRegistry, error) {
	registries := make([]monitoredRegistry, 0, len(values))

	for _, value := range values {
		registry, err := parseMonitoredRegistry(value)
		if err != nil {
			return nil, err
		}

		if registry.endpoint == "" {
			continue
		}

		registries = append(registries, registry)
	}

	return registries, nil
}

func parseMonitoredRegistry(cfg RegistryConfig) (monitoredRegistry, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return monitoredRegistry{}, errors.New("invalid registry proxy entry: endpoint is required")
	}

	target, err := parseRegistryTarget(cfg.Endpoint)
	if err != nil {
		return monitoredRegistry{}, err
	}

	ips, err := normalizeRegistryIPs(cfg.IPs, cfg.Endpoint)
	if err != nil {
		return monitoredRegistry{}, err
	}

	repository := normalizeRepository(cfg.Repository)
	if repository == "" {
		return monitoredRegistry{}, fmt.Errorf(
			"invalid registry proxy entry %q: repository is required",
			cfg.Endpoint,
		)
	}

	reference := strings.TrimSpace(cfg.Reference)
	if reference == "" {
		return monitoredRegistry{}, fmt.Errorf(
			"invalid registry proxy entry %q: reference is required",
			cfg.Endpoint,
		)
	}

	manifestAcceptHeader := strings.TrimSpace(cfg.ManifestAcceptHeader)
	if manifestAcceptHeader == "" {
		manifestAcceptHeader = defaultManifestMediaType
	}

	headers := map[string]string{}
	for name, value := range cfg.Headers {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		headers[name] = value
	}

	return monitoredRegistry{
		endpoint:             strings.TrimSpace(cfg.Endpoint),
		info:                 strings.TrimSpace(cfg.Info),
		target:               target,
		ips:                  ips,
		repository:           repository,
		reference:            reference,
		manifestAcceptHeader: manifestAcceptHeader,
		headers:              headers,
	}, nil
}

func normalizeRegistryIPs(values []string, endpoint string) ([]string, error) {
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
				"invalid registry proxy entry %q: invalid ip %q",
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

func parseRegistryTarget(endpoint string) (registryTarget, error) {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return registryTarget{}, nil
	}

	if !strings.Contains(raw, "://") {
		return registryTarget{}, fmt.Errorf(
			"invalid registry proxy endpoint %q: endpoint must include http or https scheme",
			endpoint,
		)
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return registryTarget{}, fmt.Errorf("invalid registry proxy endpoint %q: %w", endpoint, err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return registryTarget{}, fmt.Errorf(
			"invalid registry proxy endpoint %q: scheme and host are required",
			endpoint,
		)
	}

	scheme := normalizeRegistryScheme(parsed.Scheme)
	if err := validateRegistryScheme(scheme); err != nil {
		return registryTarget{}, err
	}

	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		parsed.User != nil {
		return registryTarget{}, fmt.Errorf(
			"invalid registry proxy endpoint %q: only scheme, host, and port are supported",
			endpoint,
		)
	}

	host, port, err := splitRegistryHostPort(parsed.Host, endpoint)
	if err != nil {
		return registryTarget{}, err
	}

	return newRegistryTarget(scheme, host, port, endpoint)
}

func splitRegistryHostPort(raw, original string) (string, int, error) {
	host, port, err := net.SplitHostPort(raw)
	if err == nil {
		portNum, err := parsePort(port, original)
		if err != nil {
			return "", 0, err
		}

		return host, portNum, nil
	}

	var addrErr *net.AddrError
	if errors.As(err, &addrErr) {
		switch addrErr.Err {
		case "missing port in address":
			return "", 0, fmt.Errorf(
				"invalid registry proxy endpoint %q: port is required",
				original,
			)
		case "too many colons in address":
			return "", 0, fmt.Errorf(
				"invalid registry proxy endpoint %q: IPv6 addresses with ports must use [host]:port",
				original,
			)
		}
	}

	return "", 0, fmt.Errorf("invalid registry proxy endpoint %q: %w", original, err)
}

func newRegistryTarget(scheme, host string, port int, original string) (registryTarget, error) {
	host = strings.TrimSpace(host)

	host = strings.Trim(host, "[]")
	if host == "" {
		return registryTarget{}, fmt.Errorf(
			"invalid registry proxy endpoint %q: host is required",
			original,
		)
	}

	if port < 1 || port > 65535 {
		return registryTarget{}, fmt.Errorf(
			"invalid registry proxy endpoint %q: port %d out of range",
			original,
			port,
		)
	}

	return registryTarget{
		Scheme: scheme,
		Host:   host,
		Port:   port,
	}, nil
}

func normalizeRegistryScheme(scheme string) string {
	return strings.ToLower(strings.TrimSpace(scheme))
}

func validateRegistryScheme(scheme string) error {
	switch normalizeRegistryScheme(scheme) {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf(
			"unsupported registry proxy scheme %q: only http and https are supported",
			scheme,
		)
	}
}

func normalizeRepository(repository string) string {
	repository = strings.TrimSpace(repository)
	repository = strings.Trim(repository, "/")

	return repository
}

func parsePort(port, original string) (int, error) {
	if strings.TrimSpace(port) == "" {
		return 0, fmt.Errorf("invalid registry proxy endpoint %q: port is required", original)
	}

	portNum, err := strconv.Atoi(port)
	if err != nil {
		return 0, fmt.Errorf(
			"invalid registry proxy endpoint %q: invalid port %q",
			original,
			port,
		)
	}

	if portNum < 1 || portNum > 65535 {
		return 0, fmt.Errorf(
			"invalid registry proxy endpoint %q: port %d out of range",
			original,
			portNum,
		)
	}

	return portNum, nil
}

func registryAuthority(host string, port int) string {
	if strings.Contains(host, ":") {
		return net.JoinHostPort(host, strconv.Itoa(port))
	}

	return net.JoinHostPort(host, strconv.Itoa(port))
}

func registryBaseURL(target registryTarget) string {
	return (&url.URL{
		Scheme: target.Scheme,
		Host:   registryAuthority(target.Host, target.Port),
	}).String()
}

func registryAPIURL(registry monitoredRegistry) string {
	base := registryBaseURL(registry.target)

	return base + "/v2/"
}

func registryManifestURL(registry monitoredRegistry) string {
	base := registryBaseURL(registry.target)
	repository := normalizeRepository(registry.repository)
	reference := url.PathEscape(registry.reference)

	return fmt.Sprintf("%s/v2/%s/manifests/%s", base, repository, reference)
}
