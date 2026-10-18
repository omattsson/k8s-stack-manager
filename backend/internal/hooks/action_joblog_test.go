package hooks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jobLogServer records the last job log request and answers with a handler.
type jobLogServer struct {
	mu      sync.Mutex
	lastReq *http.Request
	srv     *httptest.Server
}

func newJobLogServer(t *testing.T, h http.HandlerFunc) *jobLogServer {
	t.Helper()
	s := &jobLogServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.lastReq = r.Clone(context.Background())
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *jobLogServer) last() *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastReq
}

func newJobLogRegistry(t *testing.T, s *jobLogServer, secret string) *ActionRegistry {
	t.Helper()
	r, err := NewActionRegistry([]ActionSubscription{
		{Name: "refresh-db", URL: s.srv.URL + "/actions/refresh-db", LogPath: "/jobs/{job_id}/log", Secret: secret, TimeoutSeconds: 5},
		{Name: "no-log", URL: s.srv.URL},
	}, s.srv.Client())
	require.NoError(t, err)
	return r
}

func TestValidJobID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		id   string
		want bool
	}{
		{"job-0123456789ab", true},
		{"a", true},
		{"A.b_c-1", true},
		{"_x", true},
		{strings.Repeat("a", 128), true},
		{strings.Repeat("a", 129), false},
		{"", false},
		{".", false},
		{"..", false},
		{".hidden", false},
		{"a/b", false},
		{"a%2Fb", false},
		{"a?b", false},
		{"a#b", false},
		{"a b", false},
		{"a\nb", false},
		{"jöb", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ValidJobID(tt.id))
		})
	}
}

func TestFetchJobLog_ProxiesWithHeadersAndSignature(t *testing.T) {
	t.Parallel()
	s := newJobLogServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set(HeaderLogOffset, "42")
		w.Header().Set(HeaderJobStatus, "Succeeded")
		_, _ = w.Write([]byte("step 1\nstep 2\n"))
	})
	r := newJobLogRegistry(t, s, "topsecret")

	jl, err := r.FetchJobLog(context.Background(), "refresh-db", "job-0123456789ab", 10, &InstanceRef{ID: "i-1"})
	require.NoError(t, err)
	assert.Equal(t, "step 1\nstep 2\n", string(jl.Log))
	assert.Equal(t, int64(42), jl.NextOffset)
	assert.Equal(t, "succeeded", jl.Status)
	assert.True(t, jl.Done)
	assert.False(t, jl.Truncated)
	assert.NotEmpty(t, jl.RequestID)

	req := s.last()
	require.NotNil(t, req)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/jobs/job-0123456789ab/log", req.URL.Path, "log_path resolves on the action host, not under the action path")
	assert.Equal(t, "10", req.URL.Query().Get("offset"))
	assert.Equal(t, "i-1", req.URL.Query().Get("instance_id"))
	ts, perr := strconv.ParseInt(req.URL.Query().Get("ts"), 10, 64)
	require.NoError(t, perr, "ts is a Unix time in seconds")
	assert.InDelta(t, time.Now().Unix(), ts, 5)
	assert.Contains(t, req.URL.RawQuery, "ts=", "ts is part of the signed request URI")
	assert.Equal(t, "action-log:refresh-db", req.Header.Get(headerEvent))
	assert.Equal(t, jl.RequestID, req.Header.Get(headerRequestID))
	assert.Equal(t, sign([]byte(req.URL.RequestURI()), "topsecret"), req.Header.Get(headerSignature))
}

func TestFetchJobLog_NoSecretNoSignature(t *testing.T) {
	t.Parallel()
	s := newJobLogServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("x\n"))
	})
	r := newJobLogRegistry(t, s, "")
	_, err := r.FetchJobLog(context.Background(), "refresh-db", "job-1", 0, nil)
	require.NoError(t, err)
	assert.Empty(t, s.last().Header.Get(headerSignature))
	assert.Empty(t, s.last().URL.Query().Get("instance_id"))
}

func TestFetchJobLog_StatusAndOffset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		offsetHdr  string
		statusHdr  string
		wantNext   int64 // -1: request offset + body length
		wantStatus string
		wantDone   bool
	}{
		{"no headers running", "line\n", "", "", 5 + 5, JobStatusRunning, false},
		{"end marker", "work\n===REFRESH-DB-END=== status=succeeded\n", "", "", -1, "succeeded", true},
		{"generic end marker failed", "===JOB-END=== status=failed\r\n", "", "", 5 + 29, "failed", true},
		{"marker not at line start ignored", "x ===JOB-END=== status=failed\n", "", "", -1, JobStatusRunning, false},
		{"header wins over marker", "===JOB-END=== status=failed\n", "", "running", 5 + 28, JobStatusRunning, false},
		{"pending is active", "", "", "pending", 5, "pending", false},
		{"bad status header falls back", "a\n", "", "DROP TABLE;", 5 + 2, JobStatusRunning, false},
		{"bad offset header falls back", "abc", "-3", "", 5 + 3, JobStatusRunning, false},
		{"non-number offset falls back", "abc", "1e9", "", 5 + 3, JobStatusRunning, false},
		{"offset header used", "abc", "7", "failed", 7, "failed", true},
		{"offset header equal to offset used", "", "5", "", 5, JobStatusRunning, false},
		{"offset header below offset falls back", "abc", "3", "", 5 + 3, JobStatusRunning, false},
		{"offset header zero below offset falls back", "abc", "0", "succeeded", 5 + 3, "succeeded", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newJobLogServer(t, func(w http.ResponseWriter, _ *http.Request) {
				if tt.offsetHdr != "" {
					w.Header().Set(HeaderLogOffset, tt.offsetHdr)
				}
				if tt.statusHdr != "" {
					w.Header().Set(HeaderJobStatus, tt.statusHdr)
				}
				_, _ = w.Write([]byte(tt.body))
			})
			r := newJobLogRegistry(t, s, "")
			jl, err := r.FetchJobLog(context.Background(), "refresh-db", "job-1", 5, nil)
			require.NoError(t, err)
			wantNext := tt.wantNext
			if wantNext < 0 {
				wantNext = 5 + int64(len(tt.body))
			}
			assert.Equal(t, wantNext, jl.NextOffset)
			assert.Equal(t, tt.wantStatus, jl.Status)
			assert.Equal(t, tt.wantDone, jl.Done)
		})
	}
}

func TestFetchJobLog_SizeCapCutsAtLastNewline(t *testing.T) {
	t.Parallel()
	line := strings.Repeat("a", 1023) + "\n"
	body := strings.Repeat(line, (maxJobLogBytes/len(line))+10) + "===JOB-END=== status=succeeded\n"
	s := newJobLogServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(HeaderJobStatus, "succeeded")
		w.Header().Set(HeaderLogOffset, "999999999")
		_, _ = w.Write([]byte(body))
	})
	r := newJobLogRegistry(t, s, "")
	jl, err := r.FetchJobLog(context.Background(), "refresh-db", "job-1", 100, nil)
	require.NoError(t, err)
	assert.True(t, jl.Truncated)
	assert.False(t, jl.Done, "more log remains, so the client must poll again")
	assert.LessOrEqual(t, len(jl.Log), maxJobLogBytes)
	assert.True(t, strings.HasSuffix(string(jl.Log), "\n"), "cut at a line end")
	assert.Equal(t, int64(100+len(jl.Log)), jl.NextOffset, "the subscriber offset is ignored for a cut chunk")
}

func TestCutChunk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"cut at newline", []byte("ab\ncd"), []byte("ab\n")},
		{"no newline ascii", []byte("abcd"), []byte("abcd")},
		{"no newline incomplete rune", []byte{'a', 0xE2, 0x82}, []byte{'a'}},
		{"no newline complete rune", []byte("a€"), []byte("a€")},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, cutChunk(tt.in))
		})
	}
}

func TestFetchJobLog_Errors(t *testing.T) {
	t.Parallel()

	statusSrv := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"internal detail"}`))
		}
	}

	tests := []struct {
		name     string
		handler  http.HandlerFunc
		action   string
		jobID    string
		offset   int64
		wantErr  error
		upstream bool
	}{
		{"unknown action", statusSrv(200), "missing", "job-1", 0, nil, false},
		{"action without log_path", statusSrv(200), "no-log", "job-1", 0, ErrNoJobLog, false},
		{"invalid job id", statusSrv(200), "refresh-db", "../etc", 0, ErrInvalidJobID, false},
		{"negative offset", statusSrv(200), "refresh-db", "job-1", -1, ErrInvalidOffset, false},
		{"subscriber 404", statusSrv(404), "refresh-db", "job-1", 0, ErrJobNotFound, false},
		{"subscriber 400", statusSrv(400), "refresh-db", "job-1", 0, ErrJobNotFound, false},
		{"subscriber 500", statusSrv(500), "refresh-db", "job-1", 0, nil, true},
		{"subscriber 401", statusSrv(401), "refresh-db", "job-1", 0, nil, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newJobLogServer(t, tt.handler)
			r := newJobLogRegistry(t, s, "")
			_, err := r.FetchJobLog(context.Background(), tt.action, tt.jobID, tt.offset, nil)
			require.Error(t, err)
			switch {
			case tt.action == "missing":
				var unk ErrUnknownAction
				assert.True(t, errors.As(err, &unk))
			case tt.upstream:
				var up ErrJobLogUpstream
				assert.True(t, errors.As(err, &up))
				assert.NotContains(t, err.Error(), "internal detail", "subscriber body is never part of the error")
			default:
				assert.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func TestFetchJobLog_RefusesRedirectToAnotherHost(t *testing.T) {
	t.Parallel()
	var otherHits atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		otherHits.Add(1)
		_, _ = w.Write([]byte("secret log of another host\n"))
	}))
	t.Cleanup(other.Close)
	s := newJobLogServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/x", http.StatusFound)
	})
	r := newJobLogRegistry(t, s, "topsecret")
	_, err := r.FetchJobLog(context.Background(), "refresh-db", "job-1", 0, nil)
	require.Error(t, err)
	var up ErrJobLogUpstream
	assert.True(t, errors.As(err, &up))
	assert.Equal(t, int64(0), otherHits.Load(), "the redirect is not followed, so the signed request never reaches the other host")
}

// followingClient follows redirects and is not an *http.Client, so
// noRedirectClient cannot change it. The host check after the call must
// still refuse the response.
type followingClient struct{ c *http.Client }

func (f followingClient) Do(req *http.Request) (*http.Response, error) { return f.c.Do(req) }

func TestFetchJobLog_HostCheckWithFollowingClient(t *testing.T) {
	t.Parallel()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("log of another host\n"))
	}))
	t.Cleanup(other.Close)
	s := newJobLogServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/x", http.StatusFound)
	})
	r, err := NewActionRegistry([]ActionSubscription{{
		Name: "x", URL: s.srv.URL, LogPath: "/jobs/{job_id}/log", TimeoutSeconds: 5,
	}}, followingClient{c: &http.Client{}})
	require.NoError(t, err)
	_, err = r.FetchJobLog(context.Background(), "x", "job-1", 0, nil)
	require.Error(t, err)
	var up ErrJobLogUpstream
	require.True(t, errors.As(err, &up))
	assert.Contains(t, up.Error(), "redirect to another host refused")
}

func TestFetchJobLog_UnreachableSubscriber(t *testing.T) {
	t.Parallel()
	r, err := NewActionRegistry([]ActionSubscription{{
		Name: "x", URL: "http://127.0.0.1:1/", LogPath: "/jobs/{job_id}/log", TimeoutSeconds: 1,
	}}, nil)
	require.NoError(t, err)
	_, err = r.FetchJobLog(context.Background(), "x", "job-1", 0, nil)
	require.Error(t, err)
	var up ErrJobLogUpstream
	assert.True(t, errors.As(err, &up))
}

// TestFetchJobLog_SplitRuneAcrossChunks: "å" is two bytes (0xC3 0xA5). The
// first chunk ends after 0xC3; the backend drops it and the next poll reads
// the whole character.
func TestFetchJobLog_SplitRuneAcrossChunks(t *testing.T) {
	t.Parallel()
	full := []byte("bl\xc3\xa5b\xc3\xa4r\n") // "blåbär\n"
	var (
		mu    sync.Mutex
		limit = 3 // the first answer ends inside "å"
	)
	s := newJobLogServer(t, func(w http.ResponseWriter, r *http.Request) {
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		mu.Lock()
		end := len(full)
		if limit > 0 {
			end, limit = limit, 0
		}
		mu.Unlock()
		_, _ = w.Write(full[off:end])
	})
	r := newJobLogRegistry(t, s, "")

	first, err := r.FetchJobLog(context.Background(), "refresh-db", "job-1", 0, nil)
	require.NoError(t, err)
	assert.Equal(t, "bl", string(first.Log), "the incomplete tail is dropped")
	assert.Equal(t, int64(2), first.NextOffset)
	assert.False(t, first.Done)

	second, err := r.FetchJobLog(context.Background(), "refresh-db", "job-1", first.NextOffset, nil)
	require.NoError(t, err)
	assert.Equal(t, "åbär\n", string(second.Log))
	assert.Equal(t, int64(len(full)), second.NextOffset)
}

func TestFetchJobLog_IncompleteRuneKeptWhenSubscriberSetsOffsetOrJobEnded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		offsetHdr string
		statusHdr string
		wantLen   int
		wantNext  int64
	}{
		{"subscriber offset", "3", "", 3, 3},
		{"job ended", "", "failed", 3, 3},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newJobLogServer(t, func(w http.ResponseWriter, _ *http.Request) {
				if tt.offsetHdr != "" {
					w.Header().Set(HeaderLogOffset, tt.offsetHdr)
				}
				if tt.statusHdr != "" {
					w.Header().Set(HeaderJobStatus, tt.statusHdr)
				}
				_, _ = w.Write([]byte("bl\xc3"))
			})
			r := newJobLogRegistry(t, s, "")
			jl, err := r.FetchJobLog(context.Background(), "refresh-db", "job-1", 0, nil)
			require.NoError(t, err)
			assert.Len(t, jl.Log, tt.wantLen)
			assert.Equal(t, tt.wantNext, jl.NextOffset)
		})
	}
}
