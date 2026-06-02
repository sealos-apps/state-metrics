package domain

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const maxConcurrentIPChecks = 10

// DomainHealth represents the overall health status of a domain
type DomainHealth struct {
	Domain       string
	ResolveOk    bool // Whether DNS resolution succeeded
	IPCount      int  // Number of IPs resolved
	HealthyIPs   int  // Number of healthy IPs (HTTP and/or Cert checks passed)
	UnhealthyIPs int  // Number of unhealthy IPs
	LastChecked  time.Time
}

// IPHealth represents the health status of a specific IP for a domain
type IPHealth struct {
	Domain string
	IP     string // Specific IP address

	// DNS check
	DNSChecked   bool
	DNSOk        bool
	DNSError     string
	DNSErrorType ErrorType

	// HTTP check
	HTTPChecked   bool
	HTTPOk        bool
	HTTPError     string
	HTTPErrorType ErrorType // Classified error type
	ResponseTime  time.Duration

	// Certificate check
	CertChecked   bool
	CertOk        bool
	CertError     string
	CertErrorType ErrorType // Classified error type
	CertExpiry    time.Duration

	LastChecked time.Time
}

// DomainChecker performs health checks on domains
type DomainChecker struct {
	timeout     time.Duration
	checkHTTP   bool
	checkDNS    bool
	checkCert   bool
	includeIPv4 bool
	includeIPv6 bool
	dialRetries int
	classifier  *ErrorClassifier
}

// NewDomainChecker creates a new domain checker
func NewDomainChecker(
	timeout time.Duration,
	checkHTTP, checkDNS, checkCert bool,
	includeIPv4, includeIPv6 bool,
	dialRetries int,
) *DomainChecker {
	return &DomainChecker{
		timeout:     timeout,
		checkHTTP:   checkHTTP,
		checkDNS:    checkDNS,
		checkCert:   checkCert,
		includeIPv4: includeIPv4,
		includeIPv6: includeIPv6,
		dialRetries: normalizeDialRetries(dialRetries),
		classifier:  NewErrorClassifier(),
	}
}

// CheckIPs performs all enabled checks on a domain for each of its IPs
func (dc *DomainChecker) CheckIPs(
	ctx context.Context,
	domain monitoredDomain,
	logger *log.Entry,
) (*DomainHealth, []*IPHealth) {
	now := time.Now()

	domainHealth := &DomainHealth{
		Domain:      domain.endpoint,
		ResolveOk:   true,
		LastChecked: now,
	}

	// First, get the IPs for the domain. Configured IPs bypass DNS resolution.
	ips := domain.ips
	if len(ips) == 0 && (dc.checkDNS || dc.checkHTTP) {
		dnsResult := checkDNSWithFilter(
			ctx,
			domain.target.Host,
			dc.timeout,
			ipFamilyFilter{
				IncludeIPv4: dc.includeIPv4,
				IncludeIPv6: dc.includeIPv6,
			},
		)
		if !dnsResult.Success {
			logger.WithFields(log.Fields{
				"domain": domain.endpoint,
				"host":   domain.target.Host,
				"port":   domain.target.Port,
				"error":  dnsResult.Error,
			}).Warn("DNS resolution failed")

			// Mark resolve as failed
			domainHealth.ResolveOk = false
			domainHealth.IPCount = 0
			domainHealth.HealthyIPs = 0

			// Return a health record indicating DNS failure
			return domainHealth, []*IPHealth{
				{
					Domain:       domain.endpoint,
					IP:           "",
					DNSChecked:   true,
					DNSOk:        false,
					DNSError:     dnsResult.Error,
					DNSErrorType: dc.classifier.ClassifyDNSError(dnsResult.Error),
					LastChecked:  now,
				},
			}
		}

		ips = dnsResult.IPs

		// Check if IP list is empty
		if len(ips) == 0 {
			logger.WithFields(log.Fields{
				"domain": domain.endpoint,
			}).Warn("DNS resolution returned no IPs")

			// Mark as resolved but with 0 IPs
			domainHealth.IPCount = 0
			domainHealth.HealthyIPs = 0

			// Return a health record indicating no IPs
			return domainHealth, []*IPHealth{
				{
					Domain:     domain.endpoint,
					IP:         "",
					DNSChecked: true,
					DNSOk:      false,
					DNSError:   "DNS resolution returned no IPs after IP family filtering",
					DNSErrorType: dc.classifier.ClassifyDNSError(
						"DNS resolution returned no IPs after IP family filtering",
					),
					LastChecked: now,
				},
			}
		}
	}

	// Preallocate and keep result order stable so callers and metrics remain predictable.
	results := make([]*IPHealth, len(ips))
	for i, ip := range ips {
		results[i] = &IPHealth{
			Domain:       domain.endpoint,
			IP:           ip,
			DNSChecked:   true,
			DNSOk:        true,
			DNSErrorType: "",
			LastChecked:  now,
		}
	}

	workerCount := min(len(ips), maxConcurrentIPChecks)
	if workerCount <= 1 {
		for i, ip := range ips {
			dc.runIPChecks(ctx, domain, ip, results[i], logger)
		}
	} else {
		indexCh := make(chan int)

		var wg sync.WaitGroup
		for range workerCount {
			wg.Go(func() {
				for i := range indexCh {
					dc.runIPChecks(ctx, domain, ips[i], results[i], logger)
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

	// Calculate domain-level health metrics
	domainHealth.IPCount = len(ips)

	healthyCount := 0
	for _, health := range results {
		// An IP is considered healthy if all enabled checks passed
		isHealthy := true
		if dc.checkHTTP && !health.HTTPOk {
			isHealthy = false
		}

		if dc.checkCert && !health.CertOk {
			isHealthy = false
		}

		if isHealthy {
			healthyCount++
		}
	}

	domainHealth.HealthyIPs = healthyCount
	domainHealth.UnhealthyIPs = domainHealth.IPCount - healthyCount

	logFields := log.Fields{
		"domain":        domain.endpoint,
		"resolvedIPs":   domainHealth.IPCount,
		"healthyIPs":    domainHealth.HealthyIPs,
		"unhealthyIPs":  domainHealth.UnhealthyIPs,
		"includeIPv4":   dc.includeIPv4,
		"includeIPv6":   dc.includeIPv6,
		"checkHTTP":     dc.checkHTTP,
		"checkCert":     dc.checkCert,
		"dialRetries":   dc.dialRetries,
		"skipTLSVerify": domain.skipTLSVerify,
	}
	if domainHealth.UnhealthyIPs > 0 {
		logger.WithFields(logFields).Warn("Domain health check completed with unhealthy IPs")
	} else {
		logger.WithFields(logFields).Debug("Domain health check completed")
	}

	return domainHealth, results
}

func (dc *DomainChecker) runIPChecks(
	ctx context.Context,
	domain monitoredDomain,
	ip string,
	health *IPHealth,
	logger *log.Entry,
) {
	if dc.checkHTTP {
		health.HTTPChecked = true
		result := checkHTTPWithIPAndOptions(
			ctx,
			domain.target.Host,
			domain.target.Port,
			ip,
			domain.skipTLSVerify,
			dc.timeout,
			dc.dialRetries,
			httpCheckOptions{
				Method:              domain.httpMethod,
				Path:                domain.httpPath,
				Headers:             domain.httpHeaders,
				ExpectedStatusCodes: domain.expectedStatusCodes,
				FollowRedirects:     domain.followHTTPRedirects,
			},
		)
		health.HTTPOk = result.Success
		health.HTTPError = result.Error
		health.ResponseTime = result.ResponseTime

		if !health.HTTPOk && health.HTTPError != "" {
			health.HTTPErrorType = dc.classifier.ClassifyHTTPError(health.HTTPError)
		} else {
			health.HTTPErrorType = ""
		}

		httpLog := logger.WithFields(log.Fields{
			"domain":       domain.endpoint,
			"ip":           ip,
			"success":      health.HTTPOk,
			"errorType":    health.HTTPErrorType,
			"error":        health.HTTPError,
			"responseTime": health.ResponseTime,
		})
		if health.HTTPOk {
			httpLog.Debug("HTTP check completed")
		} else {
			httpLog.Warn("HTTP check failed")
		}
	}

	if !dc.checkCert {
		return
	}

	health.CertChecked = true

	certInfo, certErr := getTLSCertWithIPAndRetries(
		domain.target.Host,
		domain.target.Port,
		ip,
		domain.skipTLSVerify,
		dc.timeout,
		dc.dialRetries,
	)
	if certErr != nil {
		health.CertOk = false
		health.CertError = certErr.Error()
		health.CertErrorType = dc.classifier.ClassifyCertError(health.CertError)
	} else {
		health.CertOk = certInfo.IsValid
		health.CertExpiry = certInfo.ExpiresIn

		if !certInfo.IsValid {
			health.CertError = "certificate expired or not yet valid"
			health.CertErrorType = ErrorTypeCertExpired
		} else {
			health.CertErrorType = ""
		}
	}

	certLog := logger.WithFields(log.Fields{
		"domain":    domain.endpoint,
		"ip":        ip,
		"success":   health.CertOk,
		"errorType": health.CertErrorType,
		"error":     health.CertError,
		"expiresIn": health.CertExpiry,
	})
	if health.CertOk {
		certLog.Debug("Certificate check completed")
	} else {
		certLog.Warn("Certificate check failed")
	}
}
