package imagedigest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/helm"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testUser     = "puller"
	testPassword = "s3cret-pass"
)

// fakeRegistry is a TLS go-containerregistry test registry that records the
// Authorization header of every manifest request.
type fakeRegistry struct {
	srv  *httptest.Server
	host string

	mu        sync.Mutex
	manifests []string // Authorization header of each manifest request
	requests  int
}

func newFakeRegistry(t *testing.T, requireAuth bool) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{}
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		if strings.Contains(r.URL.Path, "/manifests/") {
			f.manifests = append(f.manifests, r.Header.Get("Authorization"))
		}
		f.mu.Unlock()
		if requireAuth {
			user, pass, ok := r.BasicAuth()
			if !ok || user != testUser || pass != testPassword {
				w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	f.host = strings.TrimPrefix(f.srv.URL, "https://")
	return f
}

// push writes a random image to repo:tag and returns its digest. It resets
// the recorded requests.
func (f *fakeRegistry) push(t *testing.T, rt http.RoundTripper, repo, tag string) string {
	t.Helper()
	img, err := random.Image(64, 1)
	require.NoError(t, err)
	ref, err := name.NewTag(f.host + "/" + repo + ":" + tag)
	require.NoError(t, err)
	require.NoError(t, remote.Write(ref, img, remote.WithTransport(rt),
		remote.WithAuth(&authn.Basic{Username: testUser, Password: testPassword})))
	d, err := img.Digest()
	require.NoError(t, err)
	f.reset()
	return d.String()
}

func (f *fakeRegistry) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifests = nil
	f.requests = 0
}

func (f *fakeRegistry) snapshot() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.manifests...), f.requests
}

// trustingTransport trusts the certificates of the given test servers.
func trustingTransport(servers ...*fakeRegistry) http.RoundTripper {
	pool := x509.NewCertPool()
	for _, s := range servers {
		pool.AddCert(s.srv.Certificate())
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return tr
}

func TestResolver_Digest(t *testing.T) {
	t.Parallel()

	clusterReg := newFakeRegistry(t, true)
	otherReg := newFakeRegistry(t, false)
	notAllowed := newFakeRegistry(t, false)
	rt := trustingTransport(clusterReg, otherReg, notAllowed)

	clusterDigest := clusterReg.push(t, rt, "dev/app", "main")
	otherDigest := otherReg.push(t, rt, "team/svc", "v1")
	notAllowed.push(t, rt, "x/y", "v1")

	svc := NewService([]string{otherReg.host}, WithTransport(rt), WithTimeout(5*time.Second))
	creds := Registry{URL: "https://" + clusterReg.host + "/v2", Username: testUser, Password: testPassword}

	tests := []struct {
		name       string
		reg        *fakeRegistry
		repository string
		tag        string
		want       string
		wantReqs   bool
		wantAuth   bool
	}{
		{name: "cluster registry with credentials", reg: clusterReg, repository: clusterReg.host + "/dev/app", tag: "main", want: clusterDigest, wantReqs: true, wantAuth: true},
		{name: "allowed other registry is anonymous", reg: otherReg, repository: otherReg.host + "/team/svc", tag: "v1", want: otherDigest, wantReqs: true, wantAuth: false},
		{name: "tag not found", reg: otherReg, repository: otherReg.host + "/team/svc", tag: "missing", want: "", wantReqs: true, wantAuth: false},
		{name: "host not allowed makes no request", reg: notAllowed, repository: notAllowed.host + "/x/y", tag: "v1", want: "", wantReqs: false},
		{name: "invalid tag makes no request", reg: otherReg, repository: otherReg.host + "/team/svc", tag: "bad tag!", want: "", wantReqs: false},
		{name: "empty tag makes no request", reg: otherReg, repository: otherReg.host + "/team/svc", tag: "", want: "", wantReqs: false},
		{name: "digest reference makes no request", reg: otherReg, repository: otherReg.host + "/team/svc@sha256:abc", tag: "v1", want: "", wantReqs: false},
	}
	// Subtests share the fake registries' request counters, so they run in
	// sequence.
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			tt.reg.reset()
			got := svc.ForRegistry(creds).Digest(context.Background(), tt.repository, tt.tag)
			assert.Equal(t, tt.want, got)
			manifests, reqs := tt.reg.snapshot()
			if !tt.wantReqs {
				assert.Zero(t, reqs, "no request may reach the registry")
				return
			}
			require.NotEmpty(t, manifests)
			last := manifests[len(manifests)-1]
			if tt.wantAuth {
				assert.True(t, strings.HasPrefix(last, "Basic "), "cluster registry gets the credentials")
			} else {
				for _, h := range manifests {
					assert.Empty(t, h, "an other registry never gets an Authorization header")
				}
			}
		})
	}
}

func TestResolver_CredentialsNeverSentToOtherHost(t *testing.T) {
	t.Parallel()

	clusterReg := newFakeRegistry(t, true)
	otherReg := newFakeRegistry(t, false)
	rt := trustingTransport(clusterReg, otherReg)
	want := otherReg.push(t, rt, "app", "v2")

	// The cluster registry has credentials; the lookup on the other allowed
	// host must stay anonymous.
	svc := NewService([]string{otherReg.host}, WithTransport(rt))
	r := svc.ForRegistry(Registry{URL: clusterReg.host, Username: testUser, Password: testPassword})
	assert.Equal(t, want, r.Digest(context.Background(), otherReg.host+"/app", "v2"))

	manifests, _ := otherReg.snapshot()
	require.NotEmpty(t, manifests)
	for _, h := range manifests {
		assert.Empty(t, h)
	}
	_, clusterReqs := clusterReg.snapshot()
	assert.Zero(t, clusterReqs)
}

func TestResolver_CachesPerReference(t *testing.T) {
	t.Parallel()

	reg := newFakeRegistry(t, false)
	rt := trustingTransport(reg)
	want := reg.push(t, rt, "app", "v1")

	svc := NewService(nil, WithTransport(rt))
	r := svc.ForRegistry(Registry{URL: reg.host})
	for i := 0; i < 3; i++ {
		assert.Equal(t, want, r.Digest(context.Background(), reg.host+"/app", "v1"))
	}
	assert.Empty(t, r.Digest(context.Background(), reg.host+"/app", "missing"))
	assert.Empty(t, r.Digest(context.Background(), reg.host+"/app", "missing"))
	manifests, _ := reg.snapshot()
	assert.Len(t, manifests, 2, "one manifest request per reference, failures cached too")

	// A new resolver (a new build) asks again.
	assert.Equal(t, want, svc.ForRegistry(Registry{URL: reg.host}).Digest(context.Background(), reg.host+"/app", "v1"))
	manifests, _ = reg.snapshot()
	assert.Len(t, manifests, 3)
}

func TestResolver_Timeout(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	host := strings.TrimPrefix(srv.URL, "https://")
	svc := NewService(nil, WithTransport(srv.Client().Transport), WithTimeout(200*time.Millisecond))

	start := time.Now()
	got := svc.ForRegistry(Registry{URL: host}).Digest(context.Background(), host+"/app", "v1")
	assert.Empty(t, got)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestResolver_NilIsEmpty(t *testing.T) {
	t.Parallel()

	var svc *Service
	r := svc.ForRegistry(Registry{URL: "example.com"})
	assert.Nil(t, r)
	assert.Empty(t, r.Digest(context.Background(), "example.com/app", "v1"))
}

func TestParseHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "myregistry.azurecr.io", want: "myregistry.azurecr.io"},
		{in: " Registry.Example.com:5000 ", want: "registry.example.com:5000"},
		{in: "docker.io", want: "index.docker.io"},
		{in: "", wantErr: true},
		{in: "https://reg.example.com", wantErr: true},
		{in: "reg.example.com/path", wantErr: true},
		{in: "user@reg.example.com", wantErr: true},
		{in: ":5000", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseHost(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHostFromURL(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"", ""},
		{"myregistry.azurecr.io", "myregistry.azurecr.io"},
		{"https://myregistry.azurecr.io/v2/", "myregistry.azurecr.io"},
		{"registry.local:5000/team", "registry.local:5000"},
		{"https://user:pw@reg.example.com", "reg.example.com"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, hostFromURL(tt.in))
		})
	}
}

func TestGuardTransport_BlocksPrivateRedirectTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		host    string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"10.1.2.3", true},
		{"169.254.169.254", true},
		{"localhost", true},
		{"[::1]", true},
		{"auth.docker.io", false},
		{"8.8.8.8", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			called := false
			gt := &guardTransport{
				base: roundTripFunc(func(*http.Request) (*http.Response, error) {
					called = true
					return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
				}),
				allowed: func(string) bool { return false },
			}
			req := httptest.NewRequest(http.MethodGet, "https://"+tt.host+"/token", nil)
			resp, err := gt.RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if tt.blocked {
				assert.ErrorIs(t, err, errBlockedHost)
				assert.False(t, called)
			} else {
				assert.NoError(t, err)
				assert.True(t, called)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// slowRegistry answers /v2/ at once and runs onManifest for each manifest
// request; it counts the manifest requests.
func slowRegistry(t *testing.T, onManifest func(w http.ResponseWriter, r *http.Request)) (string, *atomic.Int32, http.RoundTripper) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			n.Add(1)
			onManifest(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "https://"), &n, srv.Client().Transport
}

func notFound(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }

func hang(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }

// TestResolver_LookupCap renders a user template that loops over imageDigest:
// at most MaxLookups requests reach the registry.
func TestResolver_LookupCap(t *testing.T) {
	t.Parallel()

	host, manifests, rt := slowRegistry(t, notFound)
	r := NewService(nil, WithTransport(rt)).ForRegistry(Registry{URL: host})
	values := `d: '{{ range $i := 1000 }}{{ imageDigest "` + host + `/app" (printf "t%d" $i) }}{{ end }}'`
	_, err := helm.NewValuesGenerator().GenerateValues(context.Background(), helm.GenerateParams{
		ChartName: "app", DefaultValues: values, Digests: r,
	})
	require.NoError(t, err)
	assert.LessOrEqual(t, manifests.Load(), int32(MaxLookups))
	assert.Positive(t, manifests.Load())
}

func TestResolver_BuildBudget(t *testing.T) {
	t.Parallel()

	host, manifests, rt := slowRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusNotFound)
	})
	r := NewService(nil, WithTransport(rt), WithBuildBudget(350*time.Millisecond)).ForRegistry(Registry{URL: host})
	start := time.Now()
	for i := 0; i < 20; i++ {
		assert.Empty(t, r.Digest(context.Background(), host+"/app", fmt.Sprintf("t%d", i)))
	}
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.LessOrEqual(t, manifests.Load(), int32(5), "no request after the build budget")
}

func TestResolver_DeadHostSkipped(t *testing.T) {
	t.Parallel()

	host, manifests, rt := slowRegistry(t, hang)
	r := NewService(nil, WithTransport(rt), WithTimeout(200*time.Millisecond)).ForRegistry(Registry{URL: host})
	start := time.Now()
	for i := 0; i < 3; i++ {
		assert.Empty(t, r.Digest(context.Background(), host+"/app", fmt.Sprintf("t%d", i)))
	}
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.EqualValues(t, 1, manifests.Load(), "a host that timed out is not asked again in the build")
}

// TestResolver_CancelledLookupNotCached: a lookup that a cancelled build cut
// short is not cached, so the build can fail closed and a retry asks again.
func TestResolver_CancelledLookupNotCached(t *testing.T) {
	t.Parallel()

	var hanging atomic.Bool
	hanging.Store(true)
	arrived := make(chan struct{}, 4)
	host, manifests, rt := slowRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		if hanging.Load() {
			arrived <- struct{}{}
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	r := NewService(nil, WithTransport(rt)).ForRegistry(Registry{URL: host})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-arrived; cancel() }()
	assert.Empty(t, r.Digest(ctx, host+"/app", "v1"))
	require.EqualValues(t, 1, manifests.Load())

	// A cancelled context makes no request at all.
	assert.Empty(t, r.Digest(ctx, host+"/app", "v2"))
	assert.EqualValues(t, 1, manifests.Load())

	hanging.Store(false)
	assert.Empty(t, r.Digest(context.Background(), host+"/app", "v1"))
	assert.EqualValues(t, 2, manifests.Load(), "the cancelled lookup was not cached")
}

// TestResolver_NoPlainHTTP: a cluster registry on a loopback address over
// plain http never gets a request (go-containerregistry would fall back to
// http there), so the credentials never travel in clear text.
func TestResolver_NoPlainHTTP(t *testing.T) {
	t.Parallel()

	var requests, withAuth atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" {
			withAuth.Add(1)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")

	svc := NewService(nil, WithTimeout(2*time.Second))
	got := svc.ForRegistry(Registry{URL: "http://" + host, Username: testUser, Password: testPassword}).
		Digest(context.Background(), host+"/app", "v1")
	assert.Empty(t, got)
	assert.Zero(t, withAuth.Load(), "no credentials over plain http")
	assert.Zero(t, requests.Load(), "no plain http request at all")
}

func TestGuardTransport_Scheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		url       string
		plainHTTP bool
		wantErr   bool
	}{
		{name: "https passes", url: "https://reg.example.com/v2/"},
		{name: "http refused", url: "http://reg.example.com/v2/", wantErr: true},
		{name: "http to private address refused", url: "http://10.0.0.5/v2/", wantErr: true},
		{name: "http allowed in tests", url: "http://reg.example.com/v2/", plainHTTP: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gt := &guardTransport{
				base: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
				}),
				allowed:   func(string) bool { return true },
				plainHTTP: tt.plainHTTP,
			}
			resp, err := gt.RoundTrip(httptest.NewRequest(http.MethodGet, tt.url, nil))
			if resp != nil {
				_ = resp.Body.Close()
			}
			if tt.wantErr {
				assert.ErrorIs(t, err, errPlainHTTP)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCut(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "abc", cut("abc"))
	long := strings.Repeat("x", 1000)
	assert.Len(t, cut(long), maxLogLen+3)
}

// TestResolver_BoundedLogsAndCache: a user template that loops over invalid
// references or hosts that are not allowed logs one warning per outcome per
// build and keeps a bounded cache (#811 review N1).
func TestResolver_BoundedLogsAndCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values string
	}{
		{name: "invalid references", values: `d: '{{ range $i := 5000 }}{{ imageDigest (printf "h%d.invalid/x" $i) "bad tag" }}{{ end }}'`},
		{name: "hosts not allowed", values: `d: '{{ range $i := 5000 }}{{ imageDigest (printf "h%d.invalid/x" $i) "t" }}{{ end }}'`},
		{name: "both", values: `d: '{{ range $i := 3000 }}{{ imageDigest (printf "h%d.invalid/x" $i) "t" }}{{ imageDigest "r/x" (printf "bad tag %d" $i) }}{{ end }}'`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			r := NewService(nil, WithLogger(logger)).ForRegistry(Registry{})
			_, err := helm.NewValuesGenerator().GenerateValues(context.Background(), helm.GenerateParams{
				ChartName: "app", DefaultValues: tt.values, Digests: r,
			})
			require.NoError(t, err)

			lines := strings.Count(buf.String(), "\n")
			assert.LessOrEqual(t, lines, 3, "at most one warning per outcome and one for the reference limit:\n%s", buf.String())
			assert.Positive(t, lines)
			r.mu.Lock()
			size := len(r.cache)
			r.mu.Unlock()
			assert.LessOrEqual(t, size, MaxLookups*maxKeysFactor)
		})
	}
}
