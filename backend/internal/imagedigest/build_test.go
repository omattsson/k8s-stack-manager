package imagedigest

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests of the fallback to the last deployed digest (#811 R1): a failed
// lookup renders the digest that the last successful deploy rendered for the
// same reference; a cancel, an invalid reference and a host that is not
// allowed do not fall back.

const lastDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestBuild_Fallback(t *testing.T) {
	t.Parallel()

	up := newFakeRegistry(t, false)
	rt := trustingTransport(up)
	upDigest := up.push(t, rt, "app", "v1")
	downHost, _, _ := slowRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	hangHost, arrived, _ := hangingRegistry(t)

	tests := []struct {
		name       string
		registry   string // cluster registry host
		repository string
		tag        string
		previous   map[string]string
		prevErr    error
		cancel     bool // cancel the build during the request
		opts       []Option
		want       string
		wantPrev   int32 // calls of the previous source
		wantStored map[string]string
		wantLog    string
	}{
		{
			name: "resolved digest is rendered and stored", registry: up.host,
			repository: up.host + "/app", tag: "v1", previous: map[string]string{up.host + "/app:v1": lastDigest},
			want: upDigest, wantPrev: 0, wantStored: map[string]string{up.host + "/app:v1": upDigest},
		},
		{
			name: "registry error renders the last deployed digest", registry: downHost,
			repository: downHost + "/app", tag: "v1", previous: map[string]string{downHost + "/app:v1": lastDigest},
			want: lastDigest, wantPrev: 1, wantStored: map[string]string{downHost + "/app:v1": lastDigest},
			wantLog: "using last deployed digest",
		},
		{
			name: "registry error without an earlier digest renders empty", registry: downHost,
			repository: downHost + "/app", tag: "v1", previous: map[string]string{downHost + "/app:other": lastDigest},
			want: "", wantPrev: 1,
		},
		{
			name: "reading the earlier digests fails: empty", registry: downHost,
			repository: downHost + "/app", tag: "v1", prevErr: errors.New("db down"),
			want: "", wantPrev: 1, wantLog: "reading the last deployed digests failed",
		},
		{
			name: "lookup cap renders the last deployed digest", registry: downHost,
			repository: downHost + "/app", tag: "v1", previous: map[string]string{downHost + "/app:v1": lastDigest},
			opts: []Option{WithBuildBudget(time.Nanosecond)},
			want: lastDigest, wantPrev: 1, wantStored: map[string]string{downHost + "/app:v1": lastDigest},
		},
		{
			name: "host not allowed: no fallback", registry: up.host,
			repository: "other.example.com/app", tag: "v1", previous: map[string]string{"other.example.com/app:v1": lastDigest},
			want: "", wantPrev: 0,
		},
		{
			name: "invalid reference: no fallback", registry: up.host,
			repository: up.host + "/app", tag: "bad tag", previous: map[string]string{up.host + "/app:bad tag": lastDigest},
			want: "", wantPrev: 0,
		},
		{
			name: "cancelled build: no fallback", registry: hangHost,
			repository: hangHost + "/app", tag: "v1", previous: map[string]string{hangHost + "/app:v1": lastDigest},
			cancel: true, want: "", wantPrev: 0,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			var mu sync.Mutex
			logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, nil))
			tel, _, reader := recordingTelemetry(t)
			opts := append([]Option{WithTransport(rt), WithLogger(logger), tel, WithTimeout(2 * time.Second)}, tt.opts...)
			svc := NewService(nil, opts...)
			var prevCalls atomic.Int32
			b := svc.NewSession().NewBuild(
				func(context.Context) Registry { return Registry{URL: tt.registry} },
				func(context.Context) (map[string]string, error) {
					prevCalls.Add(1)
					return tt.previous, tt.prevErr
				})

			ctx := context.Background()
			if tt.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				go func() { <-arrived; cancel() }()
			}
			assert.Equal(t, tt.want, b.Digest(ctx, tt.repository, tt.tag))
			// A second call of the build: cached, the previous source is
			// read once at most.
			if !tt.cancel {
				assert.Equal(t, tt.want, b.Digest(ctx, tt.repository, tt.tag))
			}
			assert.Equal(t, tt.wantPrev, prevCalls.Load())
			assert.Equal(t, tt.wantStored, b.Rendered())
			if tt.want == lastDigest {
				assert.EqualValues(t, 2, lookupCount(t, reader, outcomeFallback))
			} else {
				assert.Zero(t, lookupCount(t, reader, outcomeFallback))
			}
			mu.Lock()
			logs := buf.String()
			mu.Unlock()
			if tt.wantLog != "" {
				assert.Contains(t, logs, tt.wantLog)
				assert.Equal(t, 1, bytes.Count([]byte(logs), []byte(tt.wantLog)), "one warning per build")
			}
		})
	}
}

// hangingRegistry holds every manifest request until the client leaves;
// arrived gets one value per manifest request.
func hangingRegistry(t *testing.T) (string, chan struct{}, http.RoundTripper) {
	t.Helper()
	arrived := make(chan struct{}, 16)
	host, _, rt := slowRegistry(t, func(_ http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-r.Context().Done()
	})
	return host, arrived, rt
}

func TestBuild_NilAndKey(t *testing.T) {
	t.Parallel()

	var b *Build
	assert.Empty(t, b.Digest(context.Background(), "r/app", "v1"))
	assert.Nil(t, b.Rendered())
	var sess *Session
	assert.Nil(t, sess.NewBuild(nil, nil))
	assert.Equal(t, "r/app:v1", Key(" r/app ", " v1"))

	// Without a previous source a failed lookup renders "".
	nb := NewService(nil).NewSession().NewBuild(func(context.Context) Registry { return Registry{} }, nil)
	assert.Empty(t, nb.Digest(context.Background(), "r.example.com/app", "v1"))
	assert.Nil(t, nb.Rendered())
}

// TestBuild_BulkSharesLookupNotFallback: two builds of one session share the
// failed lookup, and each falls back to the digests of its own instance.
func TestBuild_BulkSharesLookupNotFallback(t *testing.T) {
	t.Parallel()

	host, reqs, rt := slowRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	sess := NewService(nil, WithTransport(rt)).NewSession()
	reg := func(context.Context) Registry { return Registry{URL: host} }
	other := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	b1 := sess.NewBuild(reg, func(context.Context) (map[string]string, error) {
		return map[string]string{host + "/app:v1": lastDigest}, nil
	})
	b2 := sess.NewBuild(reg, func(context.Context) (map[string]string, error) {
		return map[string]string{host + "/app:v1": other}, nil
	})
	b3 := sess.NewBuild(reg, nil)
	assert.Equal(t, lastDigest, b1.Digest(context.Background(), host+"/app", "v1"))
	assert.Equal(t, other, b2.Digest(context.Background(), host+"/app", "v1"))
	assert.Empty(t, b3.Digest(context.Background(), host+"/app", "v1"))
	require.Positive(t, reqs.Load())
	before := reqs.Load()
	b1.Digest(context.Background(), host+"/app", "v1")
	assert.Equal(t, before, reqs.Load(), "the failure is cached in the session")
}

func TestBuild_RenderedIsBounded(t *testing.T) {
	t.Parallel()

	b := &Build{rendered: make(map[string]string)}
	for i := 0; i < maxRendered+50; i++ {
		b.record(Key("r/app", string(rune('a'+i%26))+time.Duration(i).String()), lastDigest)
	}
	assert.Len(t, b.Rendered(), maxRendered)
}
