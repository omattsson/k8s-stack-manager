// Package imagedigest resolves the manifest digest of an image tag for the
// imageDigest values template function (#811). A lookup sends one manifest
// HEAD request to an allowed registry host. It never fails a render: any
// problem gives "" and a warning.
//
// Hosts: the host of the cluster's registry URL is always allowed, other hosts
// only when IMAGE_DIGEST_REGISTRIES lists them. Value overrides come from
// ordinary users, so a template must not reach any other host (SSRF). A
// redirect or token realm to a host that is not allowed must not reach a
// private address either (guard.go).
//
// Credentials: the cluster's registry user name and password go only to the
// host of the cluster's registry URL. The keychain binds them to that
// registry, so go-containerregistry sends them to that host (and to the token
// realm it names) only, never after a redirect to another host. Every other
// allowed host is contacted anonymously.
package imagedigest

import (
	"context"
	"encoding/base64"
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
	"golang.org/x/sync/singleflight"
)

// DefaultTimeout bounds one lookup, retries included.
const DefaultTimeout = 10 * time.Second

// BuildBudget bounds the time of all lookups of one build (one deploy, one
// preview, one instance of a bulk deploy). A lookup after the budget makes no
// request (the last deployed digest or "" renders, see Build).
const BuildBudget = 30 * time.Second

// MaxLookups caps the registry requests of one build. A user override can
// loop over imageDigest; past the cap there is no request. A cache hit (a
// reference that another build of the session looked up) costs no request
// and does not count.
const MaxLookups = 32

// maxKeysFactor bounds the distinct references (any outcome, cache hits
// excluded) that one build adds to the cache: MaxLookups * maxKeysFactor.
// Past it there is no lookup, no cache entry and no log line.
const maxKeysFactor = 8

// maxLogLen cuts user input (repository, tag) in log lines.
const maxLogLen = 256

// Service holds the process-wide settings: the allowed hosts
// (IMAGE_DIGEST_REGISTRIES), the lookup timeout and the HTTP transports. It is
// safe for concurrent use. A nil *Service resolves nothing.
type Service struct {
	// transport serves the allowed hosts (cluster registry, allowlist).
	transport http.RoundTripper
	// guarded serves every other host (redirect targets, token realms): it
	// checks the resolved IP at dial time (guard.go). nil when transport is
	// not an *http.Transport (some tests): transport then serves all hosts.
	guarded  http.RoundTripper
	allowed  map[string]struct{}
	lookupIP func(ctx context.Context, host string) ([]net.IPAddr, error)
	tel      *telemetry
	logger   *slog.Logger
	timeout  time.Duration
	budget   time.Duration
	// maxLookups is the per-resolver lookup cap.
	maxLookups int
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

// WithBuildBudget sets the time budget of all lookups of one build
// (default BuildBudget).
func WithBuildBudget(d time.Duration) Option {
	return func(s *Service) {
		if d > 0 {
			s.budget = d
		}
	}
}

// WithMaxLookups sets the cap of the requests of one build (default
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
// Tests use it to trust a TLS test registry. When it is an *http.Transport,
// the requests to hosts that are not allowed go through a clone of it with
// the dial-time address check (guard.go).
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
		lookupIP:   net.DefaultResolver.LookupIPAddr,
		tel:        defaultTelemetry,
	}
	for _, h := range allowedHosts {
		if norm, err := ParseHost(h); err == nil {
			s.allowed[norm] = struct{}{}
		}
	}
	for _, opt := range opts {
		opt(s)
	}
	s.guarded = newGuardedTransport(s.transport, func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return s.lookupIP(ctx, host)
	})
	return s
}

// Registry is the registry of a cluster: its URL (any form: host, host:port,
// with scheme or path) and the pull credentials.
type Registry struct {
	URL      string
	Username string
	Password string
}

// Session shares digest lookups between the builds of one request: one
// Resolver per cluster registry, whose cache and dead hosts all builds of the
// session share. The limits (MaxLookups, the reference limit, BuildBudget)
// stay per build (Build). A deploy or preview uses one session; a bulk deploy
// uses one session for the whole request. Safe for concurrent use. A nil
// *Session resolves nothing.
type Session struct {
	svc       *Service
	resolvers map[Registry]*Resolver
	mu        sync.Mutex
}

// NewSession returns a new Session. A nil Service returns nil.
func (s *Service) NewSession() *Session {
	if s == nil {
		return nil
	}
	return &Session{svc: s, resolvers: make(map[Registry]*Resolver)}
}

// ForRegistry returns the Resolver of the session for a cluster registry,
// the same one for the same registry settings.
func (s *Session) ForRegistry(reg Registry) *Resolver {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.resolvers[reg]; ok {
		return r
	}
	r := s.svc.newResolver(reg)
	s.resolvers[reg] = r
	return r
}

// ForRegistry returns a Resolver for one build (one deploy or one deploy
// preview) on a cluster with the given registry: a Resolver of a new
// Session. A nil Service returns a nil Resolver, which resolves nothing.
func (s *Service) ForRegistry(reg Registry) *Resolver {
	return s.NewSession().ForRegistry(reg)
}

func (s *Service) newResolver(reg Registry) *Resolver {
	r := &Resolver{
		svc:       s,
		own:       &limits{},
		cache:     make(map[string]Result),
		deadHosts: make(map[string]struct{}),
	}
	if host := hostFromURL(reg.URL); host != "" {
		r.clusterHost = host
		if reg.Password != "" {
			r.auth = authn.FromConfig(authn.AuthConfig{Username: reg.Username, Password: reg.Password})
			r.secrets = secretForms(reg.Username, reg.Password)
		}
	}
	return r
}

// secretForms returns the forms in which the password can show up in an
// error message: plain, URL-encoded (token form body) and inside the base64
// of a Basic Authorization header.
func secretForms(user, password string) []string {
	forms := []string{
		base64.StdEncoding.EncodeToString([]byte(user + ":" + password)),
		password,
	}
	if esc := url.QueryEscape(password); esc != password {
		forms = append(forms, esc)
	}
	return forms
}

// limits are the lookup limits of one build: MaxLookups requests, the
// reference limit and the BuildBudget deadline (set at the first request of
// the build), with one warning per outcome. A cache hit counts for none.
type limits struct {
	deadline time.Time
	keys     int // references added to the cache
	lookups  int // requests started
	mu       sync.Mutex
	// One warning per build for each of these outcomes: a user template
	// can produce any number of them.
	capWarned        bool
	budgetWarned     bool
	keysWarned       bool
	invalidWarned    bool
	notAllowedWarned bool
}

// reserve returns the end of the budget, or false when it is used up. warn
// is true for the first refusal of the build only. Call with l.mu held.
func (l *limits) reserve(now time.Time, d time.Duration) (deadline time.Time, ok, warn bool) {
	if l.deadline.IsZero() {
		l.deadline = now.Add(d)
	}
	if now.Before(l.deadline) {
		return l.deadline, true, false
	}
	warn = !l.budgetWarned
	l.budgetWarned = true
	return time.Time{}, false, warn
}

// Resolver resolves digests on one cluster registry. It is safe for
// concurrent use: concurrent calls for one reference share one request, and
// calls for different references run in parallel (no lock is held during a
// request). A host that timed out is not asked again. The limits are per
// build: Lookup and Digest use the resolver's own limits (one build), a
// Build passes its own.
type Resolver struct {
	svc       *Service
	own       *limits             // limits of Lookup and Digest
	auth      authn.Authenticator // nil: no cluster credentials
	cache     map[string]Result
	deadHosts map[string]struct{} // hosts that timed out
	// flight joins concurrent lookups of one reference.
	flight      singleflight.Group
	clusterHost string
	secrets     []string // password forms, only to redact them from errors
	mu          sync.Mutex
}

func (r *Resolver) log() *slog.Logger { return r.svc.log() }

func (s *Service) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// Status tells why a lookup gave its digest.
type Status int

const (
	// StatusRejected: invalid reference or host not allowed (or a nil
	// Resolver). The digest is "" and no earlier digest may stand in.
	StatusRejected Status = iota
	// StatusResolved: the registry answered with the digest.
	StatusResolved
	// StatusFailed: registry error, timeout, not found, or a lookup limit
	// (cap, budget, dead host, reference limit) stopped the request. The
	// digest is ""; a Build renders the last deployed digest instead.
	StatusFailed
	// StatusCancelled: the build was cancelled. The digest is ""; the
	// caller fails the build (fail closed).
	StatusCancelled
)

// Result is the outcome of one lookup.
type Result struct {
	Digest string
	Status Status
}

// reach tells who may take the result of a shared lookup.
type reach int

const (
	// reachAll: a cached result, final for every caller.
	reachAll reach = iota
	// reachOwner: one of the limits of the build that ran the lookup
	// (lookup cap, budget, reference limit) stopped it. A caller of that
	// build (same limits) takes it; a caller of another build asks again
	// under its own limits (#811 N1).
	reachOwner
	// reachNone: the cancel of the caller that ran it cut it short; every
	// caller asks again with its own context.
	reachNone
)

// flightResult is the result of one shared lookup.
type flightResult struct {
	owner *limits
	res   Result
	reach reach
}

// Digest returns the manifest digest (sha256:...) that repository:tag points
// to, or "" when it cannot be resolved (logged as a warning). It never
// returns an error, so a template that calls it always renders.
func (r *Resolver) Digest(ctx context.Context, repository, tag string) string {
	return r.Lookup(ctx, repository, tag).Digest
}

// Lookup is Digest with the status of the result (see Status).
func (r *Resolver) Lookup(ctx context.Context, repository, tag string) Result {
	if r == nil {
		return Result{Status: StatusRejected}
	}
	return r.lookup(ctx, r.own, repository, tag)
}

// lookup is Lookup with the limits of a build. A caller that joins the
// request of another build (same reference) pays nothing.
func (r *Resolver) lookup(ctx context.Context, lim *limits, repository, tag string) Result {
	key := repository + ":" + tag
	for {
		// A cancelled build renders "" without caching or logging; the
		// caller fails the build (fail closed, see the values builder).
		if ctx.Err() != nil {
			return Result{Status: StatusCancelled}
		}
		r.mu.Lock()
		res, ok := r.cache[key]
		r.mu.Unlock()
		if ok {
			return res
		}
		ch := r.flight.DoChan(key, func() (any, error) {
			res, rc := r.resolve(ctx, lim, key, repository, tag)
			return flightResult{owner: lim, res: res, reach: rc}, nil
		})
		select {
		case <-ctx.Done():
			return Result{Status: StatusCancelled}
		case v := <-ch:
			fr, _ := v.Val.(flightResult)
			if fr.reach == reachAll || (fr.reach == reachOwner && fr.owner == lim) {
				return fr.res
			}
			// The build that ran the lookup was cancelled or hit one of
			// its own limits; ask again with this build's context and
			// limits (the loop returns when the context is cancelled).
		}
	}
}

// lookupJob is a registry request that resolve runs without the lock.
type lookupJob struct {
	ref      name.Tag
	deadline time.Time
	host     string
	// budgetCut: the build budget ends before the lookup timeout. A
	// lookup that hits that deadline does not make the host dead: the
	// host may answer the other builds of the session.
	budgetCut bool
}

// resolve looks up one reference once (the singleflight of Lookup runs it).
// It returns the result and who may take it (reach). Cached (reachAll):
// registry outcomes (resolved, error, not found, a full lookup timeout), a
// dead host, and rejected references. Not cached: a stop by a limit of the
// build (lookup cap, budget, budget cut during the request, reference
// limit; reachOwner), because another build's limits may allow the lookup
// (#811 N1), and a lookup that a cancelled build cut short (reachNone).
func (r *Resolver) resolve(ctx context.Context, lim *limits, key, repository, tag string) (Result, reach) {
	r.mu.Lock()
	if res, ok := r.cache[key]; ok {
		r.mu.Unlock()
		return res, reachAll
	}
	lim.mu.Lock()
	if maxKeys := r.svc.maxLookups * maxKeysFactor; lim.keys >= maxKeys {
		r.svc.tel.recordOutcome(ctx, outcomeSkipped)
		if !lim.keysWarned {
			lim.keysWarned = true
			r.log().Warn("imageDigest: too many distinct references in one deploy, the rest are not looked up (last deployed digest or empty)",
				"max_references", maxKeys)
		}
		lim.mu.Unlock()
		r.mu.Unlock()
		return Result{Status: StatusFailed}, reachOwner
	}
	job, status, cache := r.prepare(ctx, lim, repository, tag)
	if cache {
		lim.keys++
	}
	lim.mu.Unlock()
	if job == nil {
		res := Result{Status: status}
		r.mu.Unlock()
		if !cache {
			return res, reachOwner
		}
		r.mu.Lock()
		r.cache[key] = res
		r.mu.Unlock()
		return res, reachAll
	}
	r.mu.Unlock()

	digest, deadline, err := r.head(ctx, job)

	r.mu.Lock()
	if err != nil && ctx.Err() != nil {
		// The build was cancelled: no warning, no cache entry.
		r.mu.Unlock()
		return Result{Status: StatusCancelled}, reachNone
	}
	if err != nil && deadline && job.budgetCut {
		// The budget of this build ended during the request: a limit of
		// this build, not a fact about the host or the reference.
		lim.mu.Lock()
		lim.keys--
		lim.mu.Unlock()
		r.mu.Unlock()
		r.log().Warn("imageDigest: digest lookup stopped by the lookup time budget of the deploy",
			"registry", cut(job.host), "reference", cut(job.ref.Name()))
		return Result{Status: StatusFailed}, reachOwner
	}
	if err != nil && deadline {
		// The host did not answer in time: skip it from now on.
		r.deadHosts[job.host] = struct{}{}
	}
	res := Result{Digest: digest, Status: StatusResolved}
	if err != nil {
		res = Result{Status: StatusFailed}
	}
	r.cache[key] = res
	r.mu.Unlock()
	if err != nil {
		r.log().Warn("imageDigest: digest lookup failed",
			"registry", cut(job.host), "reference", cut(job.ref.Name()), "error", cut(err.Error()))
	}
	return res, reachAll
}

// prepare checks one reference under r.mu and lim.mu and reserves a lookup
// of lim. It returns nil and the status of the result when no request is to
// be made, and whether the result may be cached for the session: not for a
// stop by a limit of the build (lookup cap, budget).
func (r *Resolver) prepare(ctx context.Context, lim *limits, repository, tag string) (*lookupJob, Status, bool) {
	repository = strings.TrimSpace(repository)
	tag = strings.TrimSpace(tag)
	if repository == "" || tag == "" || strings.Contains(repository, "@") {
		r.rejectInvalid(ctx, lim, repository, tag)
		return nil, StatusRejected, true
	}
	ref, err := name.NewTag(repository + ":" + tag)
	if err != nil || ref.TagStr() != tag {
		r.rejectInvalid(ctx, lim, repository, tag)
		return nil, StatusRejected, true
	}
	host := strings.ToLower(ref.Context().RegistryStr())
	if !r.allowedHost(host) {
		r.svc.tel.recordOutcome(ctx, outcomeNotAllowed)
		if !lim.notAllowedWarned {
			lim.notAllowedWarned = true
			r.log().Warn("imageDigest: registry host is not allowed (cluster registry or IMAGE_DIGEST_REGISTRIES), rendering an empty digest; further hosts that are not allowed in this deploy are not logged",
				"registry", cut(host), "reference", cut(ref.Name()))
		}
		return nil, StatusRejected, true
	}
	if _, dead := r.deadHosts[host]; dead {
		r.svc.tel.recordOutcome(ctx, outcomeSkipped)
		return nil, StatusFailed, true
	}
	if lim.lookups >= r.svc.maxLookups {
		r.svc.tel.recordOutcome(ctx, outcomeSkipped)
		if !lim.capWarned {
			lim.capWarned = true
			r.log().Warn("imageDigest: too many distinct lookups in one deploy, the rest are not looked up (last deployed digest or empty)",
				"max_lookups", r.svc.maxLookups)
		}
		return nil, StatusFailed, false
	}
	now := time.Now()
	end, ok, warn := lim.reserve(now, r.svc.budget)
	if !ok {
		r.svc.tel.recordOutcome(ctx, outcomeSkipped)
		if warn {
			r.log().Warn("imageDigest: lookup time budget of the deploy is used up, the rest are not looked up (last deployed digest or empty)",
				"budget", r.svc.budget)
		}
		return nil, StatusFailed, false
	}
	lim.lookups++
	job := &lookupJob{ref: ref, host: host, deadline: now.Add(r.svc.timeout)}
	if end.Before(job.deadline) {
		job.deadline, job.budgetCut = end, true
	}
	return job, StatusResolved, true
}

// head sends the manifest HEAD request of job, without r.mu. It returns the
// digest, whether the lookup hit its deadline (the lookup timeout, or the
// build budget when job.budgetCut; not a cancel of the build), and the error
// with the credentials redacted.
func (r *Resolver) head(ctx context.Context, job *lookupJob) (string, bool, error) {
	lctx, cancel := context.WithDeadline(ctx, job.deadline)
	defer cancel()
	lctx, finish := r.svc.tel.startLookupSpan(lctx, job.host)
	desc, err := remote.Head(job.ref,
		remote.WithContext(lctx),
		remote.WithAuthFromKeychain(clusterKeychain{host: r.clusterHost, auth: r.auth}),
		remote.WithTransport(&guardTransport{base: r.svc.transport, guarded: r.svc.guarded, allowed: r.allowedHost, plainHTTP: r.svc.plainHTTP}),
	)
	if err != nil {
		redacted := r.redactErr(err)
		if ctx.Err() != nil {
			finish(outcomeCancelled, redacted)
			return "", false, redacted
		}
		finish(outcomeError, redacted)
		return "", errors.Is(lctx.Err(), context.DeadlineExceeded), redacted
	}
	finish(outcomeSuccess, nil)
	return desc.Digest.String(), false, nil
}

// rejectInvalid counts an invalid reference and logs the first one of the
// build. Call with lim.mu held.
func (r *Resolver) rejectInvalid(ctx context.Context, lim *limits, repository, tag string) {
	r.svc.tel.recordOutcome(ctx, outcomeInvalid)
	if lim.invalidWarned {
		return
	}
	lim.invalidWarned = true
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

// redact removes the registry password (plain, URL-encoded, or in a Basic
// header) from msg, should a registry or transport error ever echo it.
func (r *Resolver) redact(msg string) string {
	for _, s := range r.secrets {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "[REDACTED]")
		}
	}
	return msg
}

// redactErr returns err with the password redacted from its message. The log
// line and the span get only this error, never err.
func (r *Resolver) redactErr(err error) error {
	return errors.New(r.redact(err.Error()))
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
