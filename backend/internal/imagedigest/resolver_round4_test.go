package imagedigest

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Tests of review round 4 of #811: redacted spans, the dial-time address
// guard, singleflight without a lock during requests, the cancelled outcome,
// sessions, redirects and the default timeout.

// recordingTelemetry returns an Option that makes a Service record its spans
// and metrics in the returned recorder and reader (no global state).
func recordingTelemetry(t *testing.T) (Option, *tracetest.SpanRecorder, *sdkmetric.ManualReader) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		_ = mp.Shutdown(context.Background())
	})
	return func(s *Service) { s.tel = newTelemetry(tp, mp) }, sr, reader
}

// lookupCount returns the imagedigest.lookups_total sum for an outcome.
func lookupCount(t *testing.T, reader *sdkmetric.ManualReader, outcome string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var n int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "imagedigest.lookups_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key(attrOutcome)); ok && v.AsString() == outcome {
					n += dp.Value
				}
			}
		}
	}
	return n
}

// spanErrorText returns all span status descriptions, event names and event
// attributes of the ended spans, for a leak check.
func spanErrorText(spans []sdktrace.ReadOnlySpan) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.Status().Description)
		for _, ev := range s.Events() {
			b.WriteString(ev.Name)
			for _, a := range ev.Attributes {
				b.WriteString(" " + a.Value.Emit())
			}
		}
	}
	return b.String()
}

// TestNewService_DefaultTimeout (T4): without WithTimeout a lookup has the
// 10 s DefaultTimeout.
func TestNewService_DefaultTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []Option
		want time.Duration
	}{
		{name: "default", want: 10 * time.Second},
		{name: "zero keeps the default", opts: []Option{WithTimeout(0)}, want: 10 * time.Second},
		{name: "explicit", opts: []Option{WithTimeout(time.Second)}, want: time.Second},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, 10*time.Second, DefaultTimeout)
			assert.Equal(t, tt.want, NewService(nil, tt.opts...).timeout)
		})
	}
}

// echoingRegistry is a TLS registry with bearer auth whose token endpoint
// answers 401 with a body that echoes the Authorization header and the
// password it got.
func echoingRegistry(t *testing.T) (host string, rt http.RoundTripper) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			_, pass, _ := r.BasicAuth()
			if pass == "" {
				_ = r.ParseForm()
				pass = r.PostForm.Get("password")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"errors":[{"code":"UNAUTHORIZED","message":"bad credentials header=%s password=%s"}]}`,
				r.Header.Get("Authorization"), pass)
		default:
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test"`)
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "https://"), srv.Client().Transport
}

// TestResolver_FailedLookupRedactsPassword (S1, T1): a registry error that
// echoes the password (plain or in the Basic header) reaches neither the log
// nor the span.
func TestResolver_FailedLookupRedactsPassword(t *testing.T) {
	t.Parallel()

	host, rt := echoingRegistry(t)
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, nil))
	tel, sr, _ := recordingTelemetry(t)
	svc := NewService(nil, WithTransport(rt), WithLogger(logger), tel)

	got := svc.ForRegistry(Registry{URL: host, Username: testUser, Password: testPassword}).
		Digest(context.Background(), host+"/app", "v1")
	assert.Empty(t, got)

	basic := base64.StdEncoding.EncodeToString([]byte(testUser + ":" + testPassword))
	mu.Lock()
	logs := buf.String()
	mu.Unlock()
	spans := sr.Ended()
	require.Len(t, spans, 1)
	spanText := spanErrorText(spans)

	for name, text := range map[string]string{"log": logs, "span": spanText} {
		assert.Contains(t, text, "[REDACTED]", "%s: the registry echoed the password, so it was redacted: %s", name, text)
		assert.NotContains(t, text, testPassword, name)
		assert.NotContains(t, text, basic, name)
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestSecretForms(t *testing.T) {
	t.Parallel()

	r := NewService(nil).ForRegistry(Registry{URL: "reg.example.com", Username: "u", Password: "p&ss w"})
	basic := base64.StdEncoding.EncodeToString([]byte("u:p&ss w"))
	msg := "a p&ss w b p%26ss+w c " + basic
	got := r.redact(msg)
	assert.NotContains(t, got, "p&ss w")
	assert.NotContains(t, got, "p%26ss+w")
	assert.NotContains(t, got, basic)
	assert.Equal(t, 3, strings.Count(got, "[REDACTED]"))

	// No password: nothing to redact.
	assert.Equal(t, msg, NewService(nil).ForRegistry(Registry{URL: "reg.example.com"}).redact(msg))
}

func TestBlockedIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"127.10.0.1", true},
		{"::1", true},
		{"10.0.0.1", true},
		{"172.16.5.4", true},
		{"192.168.1.1", true},
		{"fd00::1", true},
		{"fc00::1", true},
		{"169.254.169.254", true},
		{"fe80::1", true},
		{"100.64.0.1", true},
		{"100.127.255.254", true},
		{"0.0.0.0", true},
		{"0.1.2.3", true},
		{"::", true},
		{"::ffff:127.0.0.1", true},
		{"::ffff:10.0.0.1", true},
		{"::127.0.0.1", true},
		{"::10.0.0.1", true},
		{"64:ff9b::127.0.0.1", true},
		{"64:ff9b::a9fe:a9fe", true},
		{"64:ff9b::10.1.2.3", true},
		{"64:ff9b::8.8.8.8", false},
		{"64:ff9b:1::1", true},
		{"64:ff9b:1:abcd::8.8.8.8", true},
		{"2002:7f00:1::1", true},
		{"2002:a9fe:a9fe::1", true},
		{"2002:c0a8:101::1", true},
		{"2002:6440:1::1", true},
		{"2002:808:808::1", false},
		{"::8.8.8.8", false},
		{"100.128.0.1", false},
		{"100.63.255.255", false},
		{"8.8.8.8", false},
		{"2001:4860:4860::8888", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.ip, func(t *testing.T) {
			t.Parallel()
			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip)
			assert.Equal(t, tt.blocked, blockedIP(ip))
		})
	}
}

// TestGuardDialer covers the dial-time check: a name that resolves to a
// blocked address (also one of several) is refused before any connect; a
// proxy host is dialed as is.
func TestGuardDialer(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	ipAddrs := func(ips ...string) []net.IPAddr {
		out := make([]net.IPAddr, 0, len(ips))
		for _, s := range ips {
			out = append(out, net.IPAddr{IP: net.ParseIP(s)})
		}
		return out
	}
	tests := []struct {
		name      string
		host      string
		lookup    []net.IPAddr
		proxy     bool
		otherPort bool // the proxy is registered on another port
		wantErr   bool
	}{
		{name: "name resolving to loopback", host: "evil.test", lookup: ipAddrs("127.0.0.1"), wantErr: true},
		{name: "name resolving to metadata address", host: "evil.test", lookup: ipAddrs("169.254.169.254"), wantErr: true},
		{name: "one public and one private address", host: "evil.test", lookup: ipAddrs("8.8.8.8", "10.0.0.1"), wantErr: true},
		{name: "loopback literal", host: "127.0.0.1", wantErr: true},
		{name: "proxy host is dialed", host: "127.0.0.1", proxy: true},
		{name: "another port of the proxy host is checked", host: "127.0.0.1", proxy: true, otherPort: true, wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &guardDialer{
				lookup:  func(context.Context, string) ([]net.IPAddr, error) { return tt.lookup, nil },
				plain:   &net.Dialer{Timeout: time.Second},
				checked: &net.Dialer{Timeout: time.Second, Control: controlBlocked},
			}
			if tt.proxy {
				proxyPort := port
				if tt.otherPort {
					proxyPort = "1"
				}
				d.proxies.Store(net.JoinHostPort(tt.host, proxyPort), struct{}{})
			}
			conn, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort(tt.host, port))
			if tt.wantErr {
				assert.ErrorIs(t, err, errBlockedHost)
				assert.Nil(t, conn)
				return
			}
			require.NoError(t, err)
			_ = conn.Close()
		})
	}
}

func TestProxyAddr(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"http://Proxy.example.com", "proxy.example.com:80"},
		{"https://proxy.example.com", "proxy.example.com:443"},
		{"socks5://proxy.example.com", "proxy.example.com:1080"},
		{"http://10.0.0.1:3128", "10.0.0.1:3128"},
		{"http://[fd00::1]:3128", "[fd00::1]:3128"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			u, err := url.Parse(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, proxyAddr(u))
		})
	}
}

func TestControlBlocked(t *testing.T) {
	t.Parallel()
	assert.ErrorIs(t, controlBlocked("tcp", "127.0.0.1:443", nil), errBlockedHost)
	assert.ErrorIs(t, controlBlocked("tcp", "[fd00::1]:443", nil), errBlockedHost)
	assert.NoError(t, controlBlocked("tcp", "8.8.8.8:443", nil))
}

// TestResolver_RedirectToPrivateNameRefused (S2): the allowed cluster
// registry (on loopback, allowed) redirects the manifest request to a name
// that is not allowed and resolves to 127.0.0.1. The dial-time guard refuses
// it, so the target never gets a request.
func TestResolver_RedirectToPrivateNameRefused(t *testing.T) {
	t.Parallel()

	target := newFakeRegistry(t, false)
	_, targetPort, err := net.SplitHostPort(target.host)
	require.NoError(t, err)
	evil := "internal.evil.test:" + targetPort

	cluster := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			http.Redirect(w, r, "https://"+evil+r.URL.Path, http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(cluster.Close)
	clusterHost := strings.TrimPrefix(cluster.URL, "https://")

	var lookups atomic.Int32
	resolveEvil := func(s *Service) {
		s.lookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
			lookups.Add(1)
			if host == "internal.evil.test" {
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
			}
			return nil, fmt.Errorf("unknown host %s", host)
		}
	}
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, nil))
	svc := NewService(nil, WithTransport(trustingTransport(target, &fakeRegistry{srv: cluster})), WithLogger(logger), resolveEvil)

	got := svc.ForRegistry(Registry{URL: clusterHost}).Digest(context.Background(), clusterHost+"/app", "v1")
	assert.Empty(t, got)
	_, reqs := target.snapshot()
	assert.Zero(t, reqs, "the private redirect target gets no request")
	assert.Positive(t, lookups.Load(), "the redirect target name went through the guarded dialer")
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, buf.String(), "private or loopback address refused")
}

// TestResolver_RedirectToOtherAllowedHostIsAnonymous (T2): the cluster
// registry redirects the manifest HEAD to another allowed registry; that
// registry gets no Authorization header.
func TestResolver_RedirectToOtherAllowedHostIsAnonymous(t *testing.T) {
	t.Parallel()

	other := newFakeRegistry(t, false)
	var clusterAuth atomic.Int32
	cluster := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != testUser || pass != testPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		clusterAuth.Add(1)
		if strings.Contains(r.URL.Path, "/manifests/") {
			http.Redirect(w, r, "https://"+other.host+r.URL.Path, http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(cluster.Close)
	clusterHost := strings.TrimPrefix(cluster.URL, "https://")

	rt := trustingTransport(other, &fakeRegistry{srv: cluster})
	want := other.push(t, rt, "app", "v1")
	svc := NewService([]string{other.host}, WithTransport(rt))

	got := svc.ForRegistry(Registry{URL: clusterHost, Username: testUser, Password: testPassword}).
		Digest(context.Background(), clusterHost+"/app", "v1")
	assert.Equal(t, want, got)
	assert.Positive(t, clusterAuth.Load(), "the cluster registry got the credentials")
	manifests, _ := other.snapshot()
	require.NotEmpty(t, manifests, "the redirect reached the other registry")
	for _, h := range manifests {
		assert.Empty(t, h, "the other allowed registry never gets an Authorization header")
	}
}

// TestResolver_ConcurrentSameReferenceOneRequest (S5): concurrent calls for
// one reference share one request.
func TestResolver_ConcurrentSameReferenceOneRequest(t *testing.T) {
	t.Parallel()

	host, manifests, rt := slowRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusNotFound)
	})
	r := NewService(nil, WithTransport(rt)).ForRegistry(Registry{URL: host})

	const callers = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			assert.Empty(t, r.Digest(context.Background(), host+"/app", "v1"))
		}()
	}
	close(start)
	wg.Wait()
	assert.EqualValues(t, 1, manifests.Load())
}

// TestResolver_DifferentReferencesInParallel (S5): no lock is held during a
// request, so two slow lookups take about one delay, not two.
func TestResolver_DifferentReferencesInParallel(t *testing.T) {
	t.Parallel()

	const delay = 400 * time.Millisecond
	host, manifests, rt := slowRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(delay)
		w.WriteHeader(http.StatusNotFound)
	})
	r := NewService(nil, WithTransport(rt)).ForRegistry(Registry{URL: host})

	begin := time.Now()
	var wg sync.WaitGroup
	for _, tag := range []string{"a", "b"} {
		wg.Add(1)
		go func(tag string) {
			defer wg.Done()
			assert.Empty(t, r.Digest(context.Background(), host+"/app", tag))
		}(tag)
	}
	wg.Wait()
	assert.Less(t, time.Since(begin), 2*delay, "the lookups ran in parallel")
	assert.EqualValues(t, 2, manifests.Load())
}

// TestResolver_ConcurrentLimitsExact (S5): concurrent distinct lookups never
// pass the lookup cap.
func TestResolver_ConcurrentLimitsExact(t *testing.T) {
	t.Parallel()

	host, manifests, rt := slowRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusNotFound)
	})
	r := NewService(nil, WithTransport(rt), WithMaxLookups(5)).ForRegistry(Registry{URL: host})
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Digest(context.Background(), host+"/app", fmt.Sprintf("t%d", i%20))
		}(i)
	}
	wg.Wait()
	assert.EqualValues(t, 5, manifests.Load())
}

// TestResolver_WaiterRetriesAfterLeaderCancel (S5): when the caller that ran
// a shared lookup is cancelled, a waiting caller with a live context asks
// again instead of taking the cut-short "".
func TestResolver_WaiterRetriesAfterLeaderCancel(t *testing.T) {
	t.Parallel()

	var first atomic.Bool
	first.Store(true)
	arrived := make(chan struct{}, 4)
	host, manifests, rt := slowRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		if first.CompareAndSwap(true, false) {
			arrived <- struct{}{}
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, nil))
	r := NewService(nil, WithTransport(rt), WithLogger(logger)).ForRegistry(Registry{URL: host})

	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		r.Digest(leaderCtx, host+"/app", "v1")
	}()
	<-arrived
	waiterDone := make(chan string)
	go func() { waiterDone <- r.Digest(context.Background(), host+"/app", "v1") }()
	time.Sleep(50 * time.Millisecond) // the waiter joins the flight
	cancel()
	<-leaderDone
	select {
	case got := <-waiterDone:
		assert.Empty(t, got)
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not finish")
	}
	assert.EqualValues(t, 2, manifests.Load(), "the waiter asked again")
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, buf.String(), "digest lookup failed", "the waiter's own lookup (404) is logged")
}

// TestResolver_CancelledOutcome (S6): a lookup that the build cancel cuts
// short counts as "cancelled" on the span and in lookups_total, not "error".
func TestResolver_CancelledOutcome(t *testing.T) {
	t.Parallel()

	arrived := make(chan struct{}, 1)
	host, _, rt := slowRegistry(t, func(_ http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-r.Context().Done()
	})
	tel, sr, reader := recordingTelemetry(t)
	r := NewService(nil, WithTransport(rt), tel).ForRegistry(Registry{URL: host})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-arrived; cancel() }()
	assert.Empty(t, r.Digest(ctx, host+"/app", "v1"))

	require.Eventually(t, func() bool { return len(sr.Ended()) == 1 }, 5*time.Second, 10*time.Millisecond)
	span := sr.Ended()[0]
	var outcome string
	for _, a := range span.Attributes() {
		if a.Key == attrOutcome {
			outcome = a.Value.AsString()
		}
	}
	assert.Equal(t, outcomeCancelled, outcome)
	assert.EqualValues(t, 1, lookupCount(t, reader, outcomeCancelled))
	assert.Zero(t, lookupCount(t, reader, outcomeError))
}

// headRegistry answers a manifest HEAD with the digest and delay that answer
// returns for the tag and the number of earlier requests for it ("" digest:
// 404). It counts the manifest requests per tag.
func headRegistry(t *testing.T, answer func(tag string, n int) (string, time.Duration)) (string, func(tag string) int, http.RoundTripper) {
	t.Helper()
	var mu sync.Mutex
	counts := map[string]int{}
	host, _, rt := slowRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		tag := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		n := counts[tag]
		counts[tag]++
		mu.Unlock()
		d, delay := answer(tag, n)
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if d == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Content-Length", "2")
		w.Header().Set("Docker-Content-Digest", d)
		w.WriteHeader(http.StatusOK)
	})
	return host, func(tag string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[tag]
	}, rt
}

// TestSession_SharesCacheLimitsPerBuild (S7, #811 G1, N1): a session returns
// one resolver per registry; its builds share the registry outcomes (cache,
// dead hosts), and a stop by one build's own limit (cap, budget, budget cut
// during the request) is not cached, so another build still looks the
// reference up.
func TestSession_SharesCacheLimitsPerBuild(t *testing.T) {
	t.Parallel()

	const digestK = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	tests := []struct {
		name string
		opts []Option
		// answer of the registry; build A looks up warm, then K; build B
		// looks up K.
		answer func(tag string, n int) (string, time.Duration)
		warm   []string
		wantA  string
		wantB  string
		wantK  int // requests for K in total
	}{
		{
			name:   "build A's lookup cap: build B resolves K",
			opts:   []Option{WithMaxLookups(2)},
			answer: func(tag string, _ int) (string, time.Duration) { return map[string]string{"K": digestK}[tag], 0 },
			warm:   []string{"w1", "w2"},
			wantA:  "", wantB: digestK, wantK: 1,
		},
		{
			name: "build A's budget cuts K during the request: build B resolves K",
			opts: []Option{WithBuildBudget(300 * time.Millisecond)},
			answer: func(tag string, n int) (string, time.Duration) {
				if tag == "K" && n == 0 {
					return digestK, 2 * time.Second
				}
				return digestK, 0
			},
			wantA: "", wantB: digestK, wantK: 2,
		},
		{
			name: "build A's used-up budget: build B resolves K",
			opts: []Option{WithBuildBudget(200 * time.Millisecond)},
			answer: func(tag string, _ int) (string, time.Duration) {
				if tag == "w1" {
					return "", 250 * time.Millisecond
				}
				return digestK, 0
			},
			warm:  []string{"w1"},
			wantA: "", wantB: digestK, wantK: 1,
		},
		{
			name:   "a registry error for K is cached and shared",
			answer: func(string, int) (string, time.Duration) { return "", 0 },
			wantA:  "", wantB: "", wantK: 1,
		},
		{
			name:   "a resolved K is cached and shared",
			answer: func(string, int) (string, time.Duration) { return digestK, 0 },
			wantA:  digestK, wantB: digestK, wantK: 1,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			host, count, rt := headRegistry(t, tt.answer)
			svc := NewService(nil, append([]Option{WithTransport(rt), WithTimeout(5 * time.Second)}, tt.opts...)...)
			sess := svc.NewSession()
			reg := func(context.Context) Registry { return Registry{URL: host} }

			a := sess.NewBuild(reg, nil)
			for _, w := range tt.warm {
				a.Digest(context.Background(), host+"/app", w)
			}
			assert.Equal(t, tt.wantA, a.Digest(context.Background(), host+"/app", "K"))
			b := sess.NewBuild(reg, nil)
			assert.Equal(t, tt.wantB, b.Digest(context.Background(), host+"/app", "K"))
			assert.Equal(t, tt.wantK, count("K"))
		})
	}

	hostA, _, rt := slowRegistry(t, notFound)
	hostB, _, _ := slowRegistry(t, notFound)
	sess := NewService(nil, WithTransport(rt)).NewSession()
	regA := Registry{URL: hostA}
	assert.Same(t, sess.ForRegistry(regA), sess.ForRegistry(regA))
	assert.NotSame(t, sess.ForRegistry(regA), sess.ForRegistry(Registry{URL: hostB}))
	var nilSess *Session
	assert.Nil(t, nilSess.ForRegistry(regA))
	var nilSvc *Service
	assert.Nil(t, nilSvc.NewSession())
}

// TestSession_WaiterOfAnotherBuildIgnoresLimitStop (#811 N1): a build that
// joins the in-flight lookup of another build does not take a stop by that
// build's limit; it looks the reference up under its own limits.
func TestSession_WaiterOfAnotherBuildIgnoresLimitStop(t *testing.T) {
	t.Parallel()

	const digestK = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	arrived := make(chan struct{}, 4)
	host, count, rt := headRegistry(t, func(tag string, n int) (string, time.Duration) {
		if n == 0 {
			arrived <- struct{}{}
			return digestK, 2 * time.Second // longer than build A's budget
		}
		return digestK, 0
	})
	sess := NewService(nil, WithTransport(rt), WithBuildBudget(400*time.Millisecond)).NewSession()
	reg := func(context.Context) Registry { return Registry{URL: host} }
	a, b := sess.NewBuild(reg, nil), sess.NewBuild(reg, nil)

	gotA := make(chan string, 1)
	go func() { gotA <- a.Digest(context.Background(), host+"/app", "K") }()
	<-arrived
	// b joins A's request, whose budget cuts it; b then asks itself.
	assert.Equal(t, digestK, b.Digest(context.Background(), host+"/app", "K"))
	assert.Equal(t, "", <-gotA)
	assert.Equal(t, 2, count("K"))
}

// TestBuild_LimitsPerBuild (#811 G1): one build whose template loops over
// many references does not use up the cap of another build of the session.
func TestBuild_LimitsPerBuild(t *testing.T) {
	t.Parallel()

	host, reqs, rt := slowRegistry(t, notFound)
	sess := NewService(nil, WithTransport(rt)).NewSession()
	reg := func(context.Context) Registry { return Registry{URL: host} }
	greedy := sess.NewBuild(reg, nil)
	for i := 0; i < 1000; i++ {
		greedy.Digest(context.Background(), host+"/app", fmt.Sprintf("loop%d", i))
	}
	assert.EqualValues(t, MaxLookups, reqs.Load())
	other := sess.NewBuild(reg, nil)
	other.Digest(context.Background(), host+"/app", "mine")
	assert.EqualValues(t, MaxLookups+1, reqs.Load(), "the other build still looks up")
}
