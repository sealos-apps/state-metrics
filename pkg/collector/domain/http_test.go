//nolint:testpackage // Tests need access to internal HTTP check helpers.
package domain

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckHTTPWithIPAndOptions_FollowRedirectsFallsBackToDefaultDialForDifferentHost(
	t *testing.T,
) {
	redirectTarget := newLocalTLSServerOnAddr(
		t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	)
	defer redirectTarget.Close()

	redirectURL, err := url.Parse(redirectTarget.URL)
	if err != nil {
		t.Fatalf("failed to parse redirect target URL: %v", err)
	}

	redirectURL.Host = net.JoinHostPort("localhost", redirectURL.Port())

	origin := newLocalTLSServerOnAddr(
		t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectURL.String(), http.StatusFound)
		}),
	)
	defer origin.Close()

	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatalf("failed to parse origin URL: %v", err)
	}

	result := checkHTTPWithIPAndOptions(
		context.Background(),
		originURL.Hostname(),
		mustURLPort(t, originURL),
		originURL.Hostname(),
		true,
		5*time.Second,
		1,
		httpCheckOptions{
			Method:          http.MethodGet,
			Path:            "/",
			FollowRedirects: true,
		},
	)

	if !result.Success {
		t.Fatalf("checkHTTPWithIPAndOptions() failed: %#v", result)
	}

	if result.StatusCode != http.StatusNoContent {
		t.Fatalf("result.StatusCode = %d, want %d", result.StatusCode, http.StatusNoContent)
	}

	if redirectURL.Hostname() == originURL.Hostname() {
		t.Fatal("redirect target hostname unexpectedly matches origin hostname")
	}
}

func TestCheckHTTPWithIPAndOptions_CanDisableRedirectFollowing(t *testing.T) {
	origin := newLocalTLSServerOnAddr(
		t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://localhost/next", http.StatusFound)
		}),
	)
	defer origin.Close()

	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatalf("failed to parse origin URL: %v", err)
	}

	result := checkHTTPWithIPAndOptions(
		context.Background(),
		originURL.Hostname(),
		mustURLPort(t, originURL),
		originURL.Hostname(),
		true,
		5*time.Second,
		1,
		httpCheckOptions{
			Method:          http.MethodGet,
			Path:            "/",
			FollowRedirects: false,
		},
	)

	if !result.Success {
		t.Fatalf("checkHTTPWithIPAndOptions() failed: %#v", result)
	}

	if result.StatusCode != http.StatusFound {
		t.Fatalf("result.StatusCode = %d, want %d", result.StatusCode, http.StatusFound)
	}
}

func TestCheckHTTPWithIPAndOptions_RetriesTLSHandshakeFailure(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	flakyListener := &dropFirstAcceptedConnListener{Listener: listener}
	origin := httptest.NewUnstartedServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	)
	origin.Listener = flakyListener

	origin.StartTLS()
	defer origin.Close()

	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatalf("failed to parse origin URL: %v", err)
	}

	result := checkHTTPWithIPAndOptions(
		context.Background(),
		originURL.Hostname(),
		mustURLPort(t, originURL),
		originURL.Hostname(),
		true,
		5*time.Second,
		2,
		httpCheckOptions{
			Method:          http.MethodGet,
			Path:            "/",
			FollowRedirects: true,
		},
	)

	if !result.Success {
		t.Fatalf("checkHTTPWithIPAndOptions() failed: %#v", result)
	}

	if result.StatusCode != http.StatusNoContent {
		t.Fatalf("result.StatusCode = %d, want %d", result.StatusCode, http.StatusNoContent)
	}

	if !flakyListener.dropped.Load() {
		t.Fatal("expected listener to drop the first accepted connection")
	}
}

func TestCheckHTTPWithIPAndOptions_UsesRequestOptions(t *testing.T) {
	origin := newLocalTLSServerOnAddr(
		t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("request method = %q, want %q", r.Method, http.MethodPost)
			}

			if r.URL.RequestURI() != "/ready?probe=domain" {
				t.Errorf("request URI = %q, want /ready?probe=domain", r.URL.RequestURI())
			}

			if r.Header.Get("X-Probe") != "domain" {
				t.Errorf("X-Probe header = %q, want domain", r.Header.Get("X-Probe"))
			}

			w.WriteHeader(http.StatusCreated)
		}),
	)
	defer origin.Close()

	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatalf("failed to parse origin URL: %v", err)
	}

	result := checkHTTPWithIPAndOptions(
		context.Background(),
		originURL.Hostname(),
		mustURLPort(t, originURL),
		originURL.Hostname(),
		true,
		5*time.Second,
		1,
		httpCheckOptions{
			Method:              http.MethodPost,
			Path:                "/ready?probe=domain",
			Headers:             map[string]string{"X-Probe": "domain"},
			ExpectedStatusCodes: []int{http.StatusCreated},
			FollowRedirects:     true,
		},
	)

	if !result.Success {
		t.Fatalf("checkHTTPWithIPAndOptions() failed: %#v", result)
	}

	if result.StatusCode != http.StatusCreated {
		t.Fatalf("result.StatusCode = %d, want %d", result.StatusCode, http.StatusCreated)
	}
}

func TestCheckHTTPWithIPAndOptions_RejectsUnexpectedStatusCode(t *testing.T) {
	origin := newLocalTLSServerOnAddr(
		t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	)
	defer origin.Close()

	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatalf("failed to parse origin URL: %v", err)
	}

	result := checkHTTPWithIPAndOptions(
		context.Background(),
		originURL.Hostname(),
		mustURLPort(t, originURL),
		originURL.Hostname(),
		true,
		5*time.Second,
		1,
		httpCheckOptions{
			ExpectedStatusCodes: []int{http.StatusOK},
			FollowRedirects:     true,
		},
	)

	if result.Success {
		t.Fatalf("checkHTTPWithIPAndOptions() succeeded, want failure: %#v", result)
	}

	if result.StatusCode != http.StatusNoContent {
		t.Fatalf("result.StatusCode = %d, want %d", result.StatusCode, http.StatusNoContent)
	}
}

func TestCheckHTTPWithIPAndOptions_DoesNotWaitForResponseBody(t *testing.T) {
	bodyStarted := make(chan struct{})
	releaseBody := make(chan struct{})

	origin := newLocalTLSServerOnAddr(
		t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)

			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}

			close(bodyStarted)
			<-releaseBody
		}),
	)
	defer origin.Close()
	defer close(releaseBody)

	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatalf("failed to parse origin URL: %v", err)
	}

	start := time.Now()
	result := checkHTTPWithIPAndOptions(
		context.Background(),
		originURL.Hostname(),
		mustURLPort(t, originURL),
		originURL.Hostname(),
		true,
		5*time.Second,
		1,
		httpCheckOptions{
			Method:              http.MethodGet,
			Path:                "/",
			ExpectedStatusCodes: []int{http.StatusOK},
			FollowRedirects:     true,
		},
	)
	elapsed := time.Since(start)

	if !result.Success {
		t.Fatalf("checkHTTPWithIPAndOptions() failed: %#v", result)
	}

	select {
	case <-bodyStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("expected handler to start streaming body")
	}

	if elapsed > 500*time.Millisecond {
		t.Fatalf("check waited for response body: elapsed = %v", elapsed)
	}
}

func TestRetryHTTPDialContext_RetriesUntilSuccess(t *testing.T) {
	attempts := 0
	wantConn := &mockConn{}
	dialContext := retryHTTPDialContext(
		func(context.Context, string, string) (net.Conn, error) {
			attempts++
			if attempts < 3 {
				return nil, errors.New("temporary dial failure")
			}

			return wantConn, nil
		},
		3,
	)

	conn, err := dialContext(context.Background(), "tcp", "127.0.0.1:443")
	if err != nil {
		t.Fatalf("retry dial returned error: %v", err)
	}

	if conn != wantConn {
		t.Fatalf("retry dial returned %#v, want %#v", conn, wantConn)
	}

	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestRetryHTTPDialContext_StopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0

	dialContext := retryHTTPDialContext(
		func(context.Context, string, string) (net.Conn, error) {
			attempts++

			cancel()
			return nil, errors.New("temporary dial failure")
		},
		3,
	)

	if _, err := dialContext(ctx, "tcp", "127.0.0.1:443"); err == nil {
		t.Fatal("retry dial returned nil error, want error")
	}

	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestRetryHTTPDialContext_NormalizesInvalidRetries(t *testing.T) {
	attempts := 0
	dialContext := retryHTTPDialContext(
		func(context.Context, string, string) (net.Conn, error) {
			attempts++
			return nil, errors.New("dial failed")
		},
		0,
	)

	if _, err := dialContext(context.Background(), "tcp", "127.0.0.1:443"); err == nil {
		t.Fatal("retry dial returned nil error, want error")
	}

	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func newLocalTLSServerOnAddr(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on loopback: %v", err)
	}

	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.StartTLS()

	return server
}

func mustURLPort(t *testing.T, parsedURL *url.URL) int {
	t.Helper()

	port, err := strconv.Atoi(parsedURL.Port())
	if err != nil {
		t.Fatalf("failed to parse URL port %q: %v", parsedURL.Port(), err)
	}

	return port
}

type dropFirstAcceptedConnListener struct {
	net.Listener
	dropped atomic.Bool
}

func (l *dropFirstAcceptedConnListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	if l.dropped.CompareAndSwap(false, true) {
		_ = conn.Close()
	}

	return conn, nil
}

type mockConn struct{}

func (*mockConn) Read([]byte) (int, error) {
	return 0, errors.New("not implemented")
}

func (*mockConn) Write([]byte) (int, error) {
	return 0, errors.New("not implemented")
}

func (*mockConn) Close() error {
	return nil
}

func (*mockConn) LocalAddr() net.Addr {
	return mockAddr("local")
}

func (*mockConn) RemoteAddr() net.Addr {
	return mockAddr("remote")
}

func (*mockConn) SetDeadline(time.Time) error {
	return nil
}

func (*mockConn) SetReadDeadline(time.Time) error {
	return nil
}

func (*mockConn) SetWriteDeadline(time.Time) error {
	return nil
}

type mockAddr string

func (a mockAddr) Network() string {
	return string(a)
}

func (a mockAddr) String() string {
	return string(a)
}
