package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Job log proxy for asynchronous actions.
//
// An asynchronous action answers the invoke call at once with a job ID
// (for example {"job_id": "job-0123456789ab"}) and does the work in the
// background. When the action config has a log_path, the backend serves the
// job log at GET /api/v1/stack-instances/:id/actions/:name/jobs/:job_id/log
// and fetches it from the subscriber:
//
//	GET <scheme>://<host of url><log_path with {job_id}>?instance_id=<id>&offset=<n>&ts=<unix seconds>
//
// The subscriber answers with the log bytes from offset to the end (text).
// Optional response headers: X-Log-Offset (next offset; default offset +
// body length) and X-Job-Status (running, succeeded, failed, ...). Without
// X-Job-Status the backend looks for an end marker line
// "===<NAME>-END=== status=<status>" in the body.

const (
	// JobIDPlaceholder is the placeholder in log_path that the backend
	// replaces with the validated job ID.
	JobIDPlaceholder = "{job_id}"

	// maxJobLogBytes caps one job log response. A larger response is cut at
	// the last newline under the cap; the client reads the rest with the
	// next offset.
	maxJobLogBytes = 256 << 10 // 256 KiB

	// maxJobLogTimeout caps the subscriber call for one job log poll.
	maxJobLogTimeout = 10 * time.Second

	maxLogPathLen = 256

	// HeaderLogOffset is the subscriber response header with the next offset.
	HeaderLogOffset = "X-Log-Offset"
	// HeaderJobStatus is the subscriber response header with the job state.
	HeaderJobStatus = "X-Job-Status"

	// JobStatusRunning is the status when the subscriber reports no end.
	JobStatusRunning = "running"
)

var (
	// jobIDPattern: one path segment of safe characters. The first character
	// is not a dot, so "." and ".." can never form a path segment.
	jobIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}$`)
	// logPathPattern: an absolute path of unreserved characters plus the
	// placeholder braces. No query, fragment, percent-encoding or host.
	logPathPattern   = regexp.MustCompile(`^/[A-Za-z0-9._~/{}-]*$`)
	jobStatusPattern = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)
	// endMarkerPattern matches a terminal line such as
	// "===JOB-END=== status=succeeded" or "===MY-ACTION-END=== status=failed".
	endMarkerPattern = regexp.MustCompile(`(?m)^===[A-Z0-9_-]*END=== status=([a-z_]{1,32})[ \t\r]*$`)
)

// Job log errors. The handler maps them to HTTP status codes; none of their
// messages contain the subscriber URL.
var (
	// ErrInvalidJobID: the job ID does not match the allowed format (400).
	ErrInvalidJobID = errors.New("invalid job id")
	// ErrInvalidOffset: the offset is negative (400).
	ErrInvalidOffset = errors.New("invalid offset")
	// ErrNoJobLog: the action has no log_path (404).
	ErrNoJobLog = errors.New("action has no job log")
	// ErrJobNotFound: the subscriber does not know the job (404).
	ErrJobNotFound = errors.New("job log not found")
)

// ValidJobID reports whether id is an allowed job ID: 1 to 128 characters of
// A-Z, a-z, 0-9, '.', '_' and '-', not starting with a dot.
func ValidJobID(id string) bool { return jobIDPattern.MatchString(id) }

// validateLogPath checks the optional log_path template at startup.
func validateLogPath(p string) error {
	if p == "" {
		return nil
	}
	if len(p) > maxLogPathLen {
		return fmt.Errorf("log_path must be at most %d characters", maxLogPathLen)
	}
	if !logPathPattern.MatchString(p) {
		return fmt.Errorf("log_path must be an absolute path (start with /) without query, fragment or encoded characters")
	}
	if strings.Count(p, JobIDPlaceholder) != 1 {
		return fmt.Errorf("log_path must contain %s exactly once", JobIDPlaceholder)
	}
	rest := strings.Replace(p, JobIDPlaceholder, "x", 1)
	if strings.ContainsAny(rest, "{}") {
		return fmt.Errorf("log_path has an unknown placeholder; only %s is allowed", JobIDPlaceholder)
	}
	if strings.Contains(rest, "//") {
		return fmt.Errorf("log_path must not contain empty segments")
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("log_path must not contain . or .. segments")
		}
	}
	return nil
}

// JobLog is one chunk of a job log, as read from the subscriber.
type JobLog struct {
	RequestID  string
	Status     string
	Log        []byte
	NextOffset int64
	// Done is true when the job has ended and this chunk holds the end of
	// the log. A client stops polling when Done is true.
	Done bool
	// Truncated is true when the subscriber returned more than the size cap.
	// The client polls again at once with NextOffset.
	Truncated bool
}

// ErrJobLogUpstream wraps a subscriber failure (transport error, timeout,
// unexpected status, redirect to another host). The handler answers 502.
type ErrJobLogUpstream struct{ Err error }

func (e ErrJobLogUpstream) Error() string { return "job log upstream: " + e.Err.Error() }
func (e ErrJobLogUpstream) Unwrap() error { return e.Err }

// jobLogURL builds the subscriber URL for a job log request. The host and
// scheme always come from the action URL; only the path and the query
// change; the query of the action URL is not sent. ts is the request time
// in Unix seconds; it is part of the signed request URI, so a subscriber can
// refuse an old (replayed) request. jobID must be valid (see ValidJobID).
func jobLogURL(sub ActionSubscription, jobID string, offset int64, instanceID string, ts int64) (*url.URL, error) {
	base, err := url.Parse(sub.URL)
	if err != nil {
		return nil, err
	}
	u := &url.URL{
		Scheme: base.Scheme,
		User:   base.User,
		Host:   base.Host,
		Path:   strings.Replace(sub.LogPath, JobIDPlaceholder, jobID, 1),
	}
	q := url.Values{}
	q.Set("offset", strconv.FormatInt(offset, 10))
	if instanceID != "" {
		q.Set("instance_id", instanceID)
	}
	q.Set("ts", strconv.FormatInt(ts, 10))
	u.RawQuery = q.Encode()
	return u, nil
}

// FetchJobLog reads the job log of an asynchronous action from the
// subscriber, from byte offset to the end, capped at maxJobLogBytes.
//
// The request is a GET with the same headers as an invoke call. When the
// action has a secret, X-StackManager-Signature is the HMAC-SHA256 of the
// request URI (path and query, exactly as sent). The query carries
// instance_id, so a subscriber can refuse a job of another instance, and ts
// (Unix seconds), so a subscriber can refuse an old request.
func (r *ActionRegistry) FetchJobLog(ctx context.Context, name, jobID string, offset int64, instance *InstanceRef) (JobLog, error) {
	sub, ok := r.Lookup(name)
	if !ok {
		return JobLog{}, ErrUnknownAction{Name: name}
	}
	if !sub.HasJobLog() {
		return JobLog{}, ErrNoJobLog
	}
	if !ValidJobID(jobID) {
		return JobLog{}, ErrInvalidJobID
	}
	if offset < 0 {
		return JobLog{}, ErrInvalidOffset
	}
	instanceID := ""
	if instance != nil {
		instanceID = instance.ID
	}
	target, err := jobLogURL(sub, jobID, offset, instanceID, r.now().Unix())
	if err != nil {
		return JobLog{}, ErrJobLogUpstream{Err: fmt.Errorf("build url: %w", err)}
	}

	requestID := newRequestID()
	ctx, _, finish := startActionLogSpan(ctx, name, requestID)
	fail := func(outcome string, err error, status int) (JobLog, error) {
		finish(outcome, err, status)
		return JobLog{RequestID: requestID}, err
	}

	timeout := time.Duration(sub.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > maxJobLogTimeout {
		timeout = maxJobLogTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return fail(outcomeTransportError, ErrJobLogUpstream{Err: fmt.Errorf("build request: %w", err)}, 0)
	}
	req.Header.Set("Accept", "text/plain, */*")
	req.Header.Set(headerEvent, "action-log:"+name)
	req.Header.Set(headerRequestID, requestID)
	if sub.Secret != "" {
		req.Header.Set(headerSignature, sign([]byte(target.RequestURI()), sub.Secret))
	}
	injectTraceContext(reqCtx, req)

	resp, err := r.client.Do(req)
	if err != nil {
		return fail(classifyErr(err), ErrJobLogUpstream{Err: err}, 0)
	}
	defer resp.Body.Close()

	// r.client does not follow redirects (a 3xx gives the 502 below). This
	// check also covers an injected client that follows them.
	if resp.Request != nil && resp.Request.URL != nil &&
		(resp.Request.URL.Host != target.Host || resp.Request.URL.Scheme != target.Scheme) {
		return fail(outcomeTransportError, ErrJobLogUpstream{Err: errors.New("redirect to another host refused")}, resp.StatusCode)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone, resp.StatusCode == http.StatusBadRequest:
		return fail(outcomeHTTPError, ErrJobNotFound, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fail(outcomeHTTPError, ErrJobLogUpstream{Err: fmt.Errorf("job log returned status %d", resp.StatusCode)}, resp.StatusCode)
	}

	body, truncated, err := readLimitedBody(resp.Body, maxJobLogBytes)
	if err != nil {
		return fail(classifyErr(err), ErrJobLogUpstream{Err: fmt.Errorf("read job log: %w", err)}, resp.StatusCode)
	}
	if truncated {
		body = cutChunk(body)
	}

	out := JobLog{
		RequestID: requestID,
		Status:    jobStatus(resp.Header.Get(HeaderJobStatus), body),
		Truncated: truncated,
	}
	// The subscriber offset counts only when the chunk is complete and the
	// offset does not go back (a smaller value could make a client loop).
	headerOffset, hasHeaderOffset := int64(0), false
	if !truncated {
		if next, perr := strconv.ParseInt(strings.TrimSpace(resp.Header.Get(HeaderLogOffset)), 10, 64); perr == nil && next >= offset {
			headerOffset, hasHeaderOffset = next, true
		}
		out.Done = !isActiveJobStatus(out.Status)
	}
	// While the job runs, a chunk can end inside a UTF-8 sequence. Without a
	// subscriber offset, drop the incomplete tail: the next poll reads it.
	if !out.Done && !hasHeaderOffset {
		body = trimIncompleteRune(body)
	}
	out.Log = body
	out.NextOffset = offset + int64(len(body))
	if hasHeaderOffset {
		out.NextOffset = headerOffset
	}
	finish(outcomeSuccess, nil, resp.StatusCode)
	return out, nil
}

// noRedirectClient returns a copy of client that does not follow
// redirects. A client that is not an *http.Client is returned unchanged.
func noRedirectClient(client httpClient) httpClient {
	c, ok := client.(*http.Client)
	if !ok {
		return client
	}
	cp := *c
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cp
}

// cutChunk shortens a capped chunk to its last complete line. Without a
// newline it drops a trailing incomplete UTF-8 sequence.
func cutChunk(b []byte) []byte {
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		return b[:i+1]
	}
	return trimIncompleteRune(b)
}

// trimIncompleteRune drops a trailing incomplete UTF-8 sequence.
func trimIncompleteRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

// jobStatus returns the job status from the X-Job-Status header, else from
// the last end marker line in the chunk, else "running".
func jobStatus(header string, body []byte) string {
	if s := strings.ToLower(strings.TrimSpace(header)); jobStatusPattern.MatchString(s) {
		return s
	}
	if m := endMarkerPattern.FindAllSubmatch(body, -1); len(m) > 0 {
		return string(m[len(m)-1][1])
	}
	return JobStatusRunning
}

// isActiveJobStatus reports whether the job still runs.
func isActiveJobStatus(s string) bool {
	switch s {
	case JobStatusRunning, "pending", "queued", "started":
		return true
	}
	return false
}
