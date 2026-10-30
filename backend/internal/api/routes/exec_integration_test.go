package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"backend/internal/models"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The no-cluster integration test of the exec route: the real SetupRoutes
// chain, real JWT auth, a real logout and the real hub revalidation. The
// command is a fake executor.

const itExecPath = "/api/v1/stack-instances/i1/exec?chart=web&container=web"

func (h *itHarness) dialExec(t *testing.T, tok itToken) *gorilla.Conn {
	t.Helper()
	c, resp, err := gorilla.DefaultDialer.Dial(h.wsURL(itExecPath), http.Header{"Authorization": []string{"Bearer " + tok.jwt}})
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	require.NoError(t, err, "dial exec")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func startMsg(t *testing.T, c *gorilla.Conn, cmd ...string) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "start", "command": cmd})
	require.NoError(t, err)
	require.NoError(t, c.WriteMessage(gorilla.TextMessage, b))
}

func (c itClusterRepoExecSwitch) set(v bool) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.r.cluster.AllowExec = v
}

type itClusterRepoExecSwitch struct{ r *itClusterRepo }

func execCloseCode(t *testing.T, c *gorilla.Conn) (int, []string) {
	t.Helper()
	var texts []string
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	for {
		mt, data, err := c.ReadMessage()
		if err == nil {
			if mt == gorilla.TextMessage {
				texts = append(texts, string(data))
			}
			continue
		}
		var ce *gorilla.CloseError
		require.True(t, errors.As(err, &ce), "want a close frame, got %v", err)
		return ce.Code, texts
	}
}

func TestExecIntegration_StreamsWhileRunningExitCodeAndAudit(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	release := make(chan struct{})
	h.exec.run = func(ctx context.Context, cmd []string, so, se io.Writer) (int, error) {
		_, _ = so.Write([]byte("out-1"))
		_, _ = se.Write([]byte("err-1"))
		_, _ = so.Write([]byte("out-2"))
		<-release
		_, _ = se.Write([]byte("err-2"))
		return 3, nil
	}
	c := h.dialExec(t, h.token(t, "owner", "user", "sid-1", time.Hour))
	startMsg(t, c, "bin/console", "cache:clear")

	// The interleaved frames arrive while the command still runs.
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, data, err := c.ReadMessage()
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"started","pod":"web-0","container":"web"}`, string(data))
	var seq []string
	for i := 0; i < 3; i++ {
		_, data, err := c.ReadMessage()
		require.NoError(t, err)
		seq = append(seq, string(data[1:])+"/"+string(rune('0'+data[0])))
	}
	assert.Equal(t, []string{"out-1/1", "err-1/2", "out-2/1"}, seq)
	assert.Equal(t, 1, h.hub.RevocableCount())

	close(release)
	code, texts := execCloseCode(t, c)
	assert.Equal(t, 1000, code)
	// err-2 is binary; the last text frame is the exit frame with code 3.
	require.NotEmpty(t, texts)
	assert.JSONEq(t, `{"type":"exit","exit_code":3}`, texts[len(texts)-1])

	require.Eventually(t, func() bool { return len(h.audit.byAction("exec_end")) == 1 }, 3*time.Second, 20*time.Millisecond)
	start := h.audit.byAction("exec")
	require.Len(t, start, 1)
	assert.Equal(t, "stack_instance", start[0].EntityType)
	assert.Equal(t, "i1", start[0].EntityID)
	var sd map[string]any
	require.NoError(t, json.Unmarshal([]byte(start[0].Details), &sd))
	assert.Equal(t, "bin/console cache:clear", sd["command"])
	assert.Equal(t, "jwt", sd["auth"])
	var ed map[string]any
	require.NoError(t, json.Unmarshal([]byte(h.audit.byAction("exec_end")[0].Details), &ed))
	assert.EqualValues(t, 3, ed["exit_code"])
	assert.Equal(t, "nonzero", ed["result"])
	assert.EqualValues(t, 10, ed["bytes_stdout"])
	assert.EqualValues(t, 10, ed["bytes_stderr"])

	// One start and one end entry, nothing from the audit middleware (GET).
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, 2, h.audit.count())
	for _, e := range h.audit.byAction("exec") {
		assert.NotContains(t, e.Details, "out-1")
	}
}

func TestExecIntegration_LogoutClosesWith1008(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	started := make(chan struct{})
	h.exec.run = func(ctx context.Context, _ []string, _, _ io.Writer) (int, error) {
		close(started)
		<-ctx.Done()
		return -1, ctx.Err()
	}
	tok := h.token(t, "owner", "user", "sid-1", time.Hour)
	c := h.dialExec(t, tok)
	startMsg(t, c, "sleep", "1000")
	<-started

	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/api/v1/auth/logout", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tok.jwt)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	code, _ := execCloseCode(t, c)
	assert.Equal(t, 1008, code)
	require.Eventually(t, func() bool { return len(h.audit.byAction("exec_end")) == 1 }, 3*time.Second, 20*time.Millisecond)
	var ed map[string]any
	require.NoError(t, json.Unmarshal([]byte(h.audit.byAction("exec_end")[0].Details), &ed))
	assert.Equal(t, "revoked", ed["result"])
	assert.Len(t, h.audit.byAction("exec"), 1)
}

func TestExecIntegration_ClusterSwitchClosesAtRevalidation(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	started := make(chan struct{})
	h.exec.run = func(ctx context.Context, _ []string, _, _ io.Writer) (int, error) {
		close(started)
		<-ctx.Done()
		return -1, ctx.Err()
	}
	c := h.dialExec(t, h.token(t, "owner", "user", "sid-1", time.Hour))
	startMsg(t, c, "sleep", "1000")
	<-started
	itClusterRepoExecSwitch{h.clusters}.set(false)
	code, _ := execCloseCode(t, c)
	assert.Equal(t, 1008, code)
}

// The command runs longer than the ReadTimeout of the http.Server: the
// deadline of the hijacked connection is cleared, so the session stays open.
func TestExecIntegration_SessionOutlivesServerReadTimeout(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{serverReadTimeout: 200 * time.Millisecond})
	h.exec.run = func(ctx context.Context, _ []string, so, _ io.Writer) (int, error) {
		select {
		case <-time.After(700 * time.Millisecond):
		case <-ctx.Done():
			return -1, ctx.Err()
		}
		_, _ = so.Write([]byte("late"))
		return 0, nil
	}
	c := h.dialExec(t, h.token(t, "owner", "user", "sid-1", time.Hour))
	startMsg(t, c, "sleep", "1")
	code, texts := execCloseCode(t, c)
	assert.Equal(t, 1000, code)
	require.NotEmpty(t, texts)
	assert.JSONEq(t, `{"type":"exit","exit_code":0}`, texts[len(texts)-1])
}

func TestExecIntegration_PreUpgradeStatuses(t *testing.T) {
	t.Parallel()
	h := newITHarness(t, itOpts{})
	tok := h.token(t, "other", "user", "sid-2", time.Hour)
	hdr := http.Header{"Authorization": []string{"Bearer " + tok.jwt}}
	status, body := h.get(t, itExecPath, hdr)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "forbidden", body["code"])

	owner := http.Header{"Authorization": []string{"Bearer " + h.token(t, "owner", "user", "sid-1", time.Hour).jwt}}
	status, body = h.get(t, "/api/v1/stack-instances/i1/exec?chart=nope", owner)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "chart_not_found", body["code"])
	status, body = h.get(t, "/api/v1/stack-instances/i1/exec?chart=web&container=db", owner)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "container_not_allowed", body["code"])
	status, body = h.get(t, itExecPath, nil)
	assert.Equal(t, http.StatusUnauthorized, status)
	_ = body

	h.instances.mutate(func(i *models.StackInstance) { i.Status = models.StackStatusStopped })
	status, body = h.get(t, itExecPath, owner)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "instance_not_deployed", body["code"])
}
