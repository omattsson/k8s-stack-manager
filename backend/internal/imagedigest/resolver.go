// Package imagedigest resolves the manifest digest of an image tag for the
// imageDigest values template function (#811). A lookup sends one manifest
// HEAD request to an allowed registry host. It never fails a render: any
// problem gives "" and a warning.
//
// Hosts: the host of the cluster's registry URL is always allowed, other hosts
// only when IMAGE_DIGEST_REGISTRIES lists them. Value overrides come from
// ordinary users, so a template must not reach any other host (SSRF).
//
// Credentials: the cluster's registry user name and password go only to the
// host of the cluster's registry URL. The keychain binds them to that
// registry, so go-containerregistry sends them to that host (and to the token
// realm it names) only, never after a redirect to another host. Every other
// allowed host is contacted anonymously.
package imagedigest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// DefaultTimeout bounds one lookup, retries included.
const DefaultTimeout = 10 * time.Second

// BuildBudget bounds the time of all lookups of one build (one deploy, one
// preview, one instance of a bulk deploy). A lookup after the budget renders
// "" without a request.
const BuildBudget = 30 * time.Second

// MaxLookups caps the distinct references that one build looks up. A user
// override can loop over imageDigest; past the cap it renders "" without a
// request.
const MaxLookups = 32

// maxKeysFactor bounds the distinct references (any outcome) that one build
// remembers: MaxLookups * maxKeysFactor. Past it imageDigest renders ""
// without a lookup, a cache entry or a log line.
const maxKeysFactor = 8

// maxLogLen cuts user input (repository, tag) in log lines.
const maxLogLen = 256

// Service holds the process-wide settings: the allowed hosts
// (IMAGE_DIGEST_REGISTRIES), the lookup timeout and the HTTP transport. It is
// safe for concurrent use. A nil *Service resolves nothing.
type Service struct {
	transport  http.RoundTripper
	allowed    map[string]struct{}
	timeout    time.Duration
	budget     time.Duration
	maxLookups int
	logger     *slog.Logger
	plainHTTP  bool
}

// Option configures a Service.
type Option func(*Service)

// WithTimeout sets the timeout of one lookup (default DefaultTimeout).
func WithTimeout(d time.Duration) Option {
	return func(s *Service) {
		if d > 0 {
			s.timeout = d
		}
	}
}

// WithBuildBudget sets the time budget of all lookups of one build (default
// BuildBudget).
func WithBuildBudget(d time.Duration) Option {
	return func(s *Service) {
		if d > 0 {
			s.budget = d
		}
	}
}

// WithMaxLookups sets the cap of distinct lookups of one build (default
// MaxLookups).
func WithMaxLookups(n int) Option {
	return func(s *Service) {
		if n > 0 {
			s.maxLookups = n
		}
	}
}

// WithPlainHTTPForTests allows plain http requests. Only tests use it: in
// production every registry request is https, so the cluster credentials
// never travel in clear text (go-containerregistry falls back to http for
// localhost, private addresses and *.local).
func WithPlainHTTPForTests() Option {
	return func(s *Service) { s.plainHTTP = true }
}

// WithLogger sets the logger of the warnings (default slog.Default()).
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// WithTransport sets the base HTTP transport (default remote.DefaultTransport).
// Tests use it to trust a TLS test registry.
func WithTransport(rt http.RoundTripper) Option {
	return func(s *Service) {
		if rt != nil {
			s.transport = rt
		}
	}
}

// NewService returns a Service. allowedHosts are the IMAGE_DIGEST_REGISTRIES
// entries (normalized with ParseHost); an entry that does not parse is
// skipped.
func NewService(allowedHosts []string, opts ...Option) *Service {
	s := &Service{
		allowed:    make(map[string]struct{}, len(allowedHosts)),
		timeout:    DefaultTimeout,
		budget:     BuildBudget,
		maxLookups: MaxLookups,
		transport:  remote.DefaultTransport,
	}
	for _, h := range allowedHosts {
		if norm, err := ParseHost(h); err == nil {
			s.allowed[norm] = struct{}{}
		}
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Registry is the registry of a cluster: its URL (any form: host, host:port,
// with scheme or path) and the pull credentials.
type Registry struct {
	URL      string
	Username string
	Password string
}

// ForRegistry returns a Resolver for one build (one deploy or one deploy
// preview) on a cluster with the given registry. The Resolver caches each
// reference, so a build makes at most one request per reference. A nil
// Service returns a nil Resolver, which resolves nothing.
func (s *Service) ForRegistry(reg Registry) *Resolver {
	if s == nil {
		return nil
	}
	r := &Resolver{
		svc:       s,
		cache:     make(map[string]string),
		deadHosts: make(map[string]struct{}),
	}
	if host := hostFromURL(reg.URL); host != "" {
		r.clusterHost = host
		if reg.Password != "" {
			r.auth = authn.FromConfig(authn.AuthConfig{Username: reg.Username, Password: reg.Password})
			r.password = reg.Password
		}
	}
	return r
}

// Resolver resolves digests for one build. It is safe for concurrent use.
// Limits of one build: MaxLookups distinct requests, BuildBudget for all of
// them, and a host that timed out is not asked again.
type Resolver struct {
	deadline    time.Time // end of the build budget, set at the first request
	svc         *Service
	auth        authn.Authenticator // nil: no cluster credentials
	cache       map[string]string
	deadHosts   map[string]struct{} // hosts that timed out in this build
	clusterHost string
	password    string // only to redact it from error messages
	lookups     int
	mu          sync.Mutex
	capWarned   bool
	budgetSpent bool
	// One warning per build for each of these outcomes: a user template
	// can produce any number of them.
	invalidWarned    bool
	notAllowedWarned bool
	keysWarned       bool
}

func (r *Resolver) log() *slog.Logger {
	if r.svc.logger != nil {
		return r.svc.logger
	}
	return slog.Default()
}

// Digest returns the manifest digest (sha256:...) that repository:tag points
// to, or "" when it cannot be resolved (logged as a warning). It never
// returns an error, so a template that calls it always renders.
func (r *Resolver) Digest(ctx context.Context, repository, tag string) string {
	if r == nil {
		return ""
	}
	// A cancelled build renders "" without caching or logging; the caller
	// fails the build (fail closed, see the values builder).
	if ctx.Err() != nil {
		return ""
	}
	key := repository + ":" + tag
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.cache[key]; ok {
		return d
	}
	if maxKeys := r.svc.maxLookups * maxKeysFactor; len(r.cache) >= maxKeys {
		recordOutcome(ctx, outcomeSkipped)
		if !r.keysWarned {
			r.keysWarned = true
			r.log().Warn("imageDigest: too many distinct references in one deploy, rendering empty digests for the rest",
				"max_references", maxKeys)
		}
		return ""
	}
	d, cache := r.lookup(ctx, repository, tag)
	if cache {
		r.cache[key] = d
	}
	return d
}

// lookup resolves one reference. It returns the digest ("" on failure) and
// whether to cache the result: a lookup that a cancelled build cut short is
// not cached.
func (r *Resolver) lookup(ctx context.Context, repository, tag string) (string, bool) {
	repository = strings.TrimSpace(repository)
	tag = strings.TrimSpace(tag)
	if repository == "" || tag == "" || strings.Contains(repository, "@") {
		r.rejectInvalid(ctx, repository, tag)
		return "", true
	}
	ref, err := name.NewTag(repository + ":" + tag)
	if err != nil || ref.TagStr() != tag {
		r.rejectInvalid(ctx, repository, tag)
		return "", true
	}
	host := strings.ToLower(ref.Context().RegistryStr())
	if !r.allowedHost(host) {
		recordOutcome(ctx, outcomeNotAllowed)
		if !r.notAllowedWarned {
			r.notAllowedWarned = true
			r.log().Warn("imageDigest: registry host is not allowed (cluster registry or IMAGE_DIGEST_REGISTRIES), rendering an empty digest; further hosts that are not allowed in this deploy are not logged",
				"registry", cut(host), "reference", cut(ref.Name()))
		}
		return "", true
	}
	if _, dead := r.deadHosts[host]; dead {
		recordOutcome(ctx, outcomeSkipped)
		return "", true
	}
	if r.lookups >= r.svc.maxLookups {
		recordOutcome(ctx, outcomeSkipped)
		if !r.capWarned {
			r.capWarned = true
			r.log().Warn("imageDigest: too many distinct lookups in one deploy, rendering empty digests for the rest",
				"max_lookups", r.svc.maxLookups)
		}
		return "", true
	}
	now := time.Now()
	if r.deadline.IsZero() {
		r.deadline = now.Add(r.svc.budget)
	}
	if !now.Before(r.deadline) {
		recordOutcome(ctx, outcomeSkipped)
		if !r.budgetSpent {
			r.budgetSpent = true
			r.log().Warn("imageDigest: lookup time budget of the deploy is used up, rendering empty digests for the rest",
				"budget", r.svc.budget)
		}
		return "", true
	}
	r.lookups++

	deadline := now.Add(r.svc.timeout)
	if r.deadline.Before(deadline) {
		deadline = r.deadline
	}
	lctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	lctx, finish := startLookupSpan(lctx, host)
	desc, err := remote.Head(ref,
		remote.WithContext(lctx),
		remote.WithAuthFromKeychain(clusterKeychain{host: r.clusterHost, auth: r.auth}),
		remote.WithTransport(&guardTransport{base: r.svc.transport, allowed: r.allowedHost, plainHTTP: r.svc.plainHTTP}),
	)
	if err != nil {
		finish(outcomeError, err)
		if ctx.Err() != nil {
			// The build was cancelled: no warning, no cache entry.
			return "", false
		}
		if errors.Is(lctx.Err(), context.DeadlineExceeded) {
			// The host did not answer in time: skip it for the rest of
			// the build.
			r.deadHosts[host] = struct{}{}
		}
		r.log().Warn("imageDigest: digest lookup failed, rendering an empty digest",
			"registry", cut(host), "reference", cut(ref.Name()), "error", cut(r.redact(err.Error())))
		return "", true
	}
	finish(outcomeSuccess, nil)
	return desc.Digest.String(), true
}

// rejectInvalid counts an invalid reference and logs the first one of the
// build.
func (r *Resolver) rejectInvalid(ctx context.Context, repository, tag string) {
	recordOutcome(ctx, outcomeInvalid)
	if r.invalidWarned {
		return
	}
	r.invalidWarned = true
	r.log().Warn("imageDigest: invalid image reference, rendering an empty digest; further invalid references in this deploy are not logged",
		"repository", cut(repository), "tag", cut(tag))
}

// cut shortens user input for a log line.
func cut(s string) string {
	if len(s) <= maxLogLen {
		return s
	}
	return s[:maxLogLen] + "..."
}

// allowedHost reports whether host is the cluster registry host or listed in
// IMAGE_DIGEST_REGISTRIES.
func (r *Resolver) allowedHost(host string) bool {
	host = strings.ToLower(host)
	if host == "" {
		return false
	}
	if host == r.clusterHost {
		return true
	}
	_, ok := r.svc.allowed[host]
	return ok
}

// redact removes the registry password from msg, should a registry or
// transport error ever echo it.
func (r *Resolver) redact(msg string) string {
	if r.password == "" {
		return msg
	}
	return strings.ReplaceAll(msg, r.password, "[REDACTED]")
}

// clusterKeychain returns the cluster credentials for the cluster registry
// host only; every other registry is anonymous.
type clusterKeychain struct {
	auth authn.Authenticator
	host string
}

func (k clusterKeychain) Resolve(res authn.Resource) (authn.Authenticator, error) {
	if k.auth != nil && k.host != "" && strings.EqualFold(res.RegistryStr(), k.host) {
		return k.auth, nil
	}
	return authn.Anonymous, nil
}

// guardTransport refuses every request that is not https (unless
// WithPlainHTTPForTests), so credentials never go in clear text:
// go-containerregistry tries plain http for localhost, private addresses and
// *.local. It also refuses a request to a host that is not allowed when that
// host is a loopback, private, link-local or unspecified address or
// localhost. Such requests come from a redirect or a token realm of an
// allowed registry; go-containerregistry checks realms the same way, this
// also covers manifest redirects.
type guardTransport struct {
	base      http.RoundTripper
	allowed   func(host string) bool
	plainHTTP bool
}

var (
	errBlockedHost = errors.New("request to a private or loopback address refused")
	errPlainHTTP   = errors.New("plain http registry request refused, https is required")
)

func (t *guardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.EqualFold(req.URL.Scheme, "https") && !t.plainHTTP {
		return nil, fmt.Errorf("%w: %s", errPlainHTTP, req.URL.Host)
	}
	if !t.allowed(req.URL.Host) && blockedHostname(req.URL.Hostname()) {
		return nil, fmt.Errorf("%w: %s", errBlockedHost, req.URL.Host)
	}
	return t.base.RoundTrip(req)
}

func blockedHostname(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast()
}

// ParseHost checks one IMAGE_DIGEST_REGISTRIES entry: a host with an optional
// port, no scheme, path or user info. It returns the lower-case registry name
// as go-containerregistry names it (docker.io becomes index.docker.io).
func ParseHost(raw string) (string, error) {
	entry := strings.TrimSpace(raw)
	if entry == "" {
		return "", errors.New("empty registry host")
	}
	if strings.Contains(entry, "://") || strings.ContainsAny(entry, "/@?# ") {
		return "", fmt.Errorf("registry %q must be a host with an optional port, without scheme or path", entry)
	}
	u, err := url.Parse("//" + entry)
	if err != nil || u.Host != entry || u.Hostname() == "" {
		return "", fmt.Errorf("registry %q is not a valid host[:port]", entry)
	}
	reg, err := name.NewRegistry(strings.ToLower(entry), name.StrictValidation)
	if err != nil {
		return "", fmt.Errorf("registry %q is not a valid registry host: %w", entry, err)
	}
	return strings.ToLower(reg.RegistryStr()), nil
}

// hostFromURL returns the normalized registry host of a cluster registry URL
// such as "myregistry.azurecr.io", "https://myregistry.azurecr.io/v2" or
// "registry:5000/path"; "" when it has none.
func hostFromURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "//" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	host, err := ParseHost(u.Host)
	if err != nil {
		return ""
	}
	return host
}
