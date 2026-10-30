package podstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/websocket"

	gorilla "github.com/gorilla/websocket"
)

// Limits and timings of an exec session (see the exec contract).
const (
	// DefaultExecStartTimeout is how long the client has to send the start
	// message.
	DefaultExecStartTimeout = 10 * time.Second
	// MaxExecArgs is the largest number of arguments of a command.
	MaxExecArgs = 256
	// ExecFrameMax is the largest binary frame (stream byte plus data).
	ExecFrameMax = 32 << 10
	// maxExecErrorMessage cuts the message of an error frame.
	maxExecErrorMessage = 200
)

// Stream bytes of the binary frames of an exec session.
const (
	ExecStreamStdout byte = 1
	ExecStreamStderr byte = 2
)

// Results of an exec session (the result field of the exec_end audit entry).
const (
	ExecResultZero         = "zero"
	ExecResultNonzero      = "nonzero"
	ExecResultError        = "error"
	ExecResultTimeout      = "timeout"
	ExecResultRevoked      = "revoked"
	ExecResultClientClosed = "client_closed"
	ExecResultShutdown     = "shutdown"
)

// ErrExecInternal marks an error of the run function that is a fault of the
// server and not of the Kubernetes stream: the client gets "Internal server
// error" and the close code 1011.
var ErrExecInternal = errors.New("internal exec error")

// ErrExecAuditUnavailable is what BeforeStart returns when the start audit
// entry could not be written: the command does not run.
var ErrExecAuditUnavailable = errors.New("audit unavailable")

// ValidateExecCommand applies the command checks of the contract: 1 to
// MaxExecArgs arguments, the first one non-empty, no NUL byte in any argument,
// and at most maxLen bytes in total. The backend never wraps the command in a
// shell.
func ValidateExecCommand(cmd []string, maxLen int) error {
	if len(cmd) == 0 {
		return errors.New("command is empty")
	}
	if len(cmd) > MaxExecArgs {
		return fmt.Errorf("command has more than %d arguments", MaxExecArgs)
	}
	if cmd[0] == "" {
		return errors.New("the first argument is empty")
	}
	total := 0
	for _, a := range cmd {
		if strings.IndexByte(a, 0) >= 0 {
			return errors.New("an argument contains a NUL byte")
		}
		total += len(a)
	}
	if total > maxLen {
		return fmt.Errorf("command is longer than %d bytes", maxLen)
	}
	return nil
}

// ExecRunFunc runs the command in the pod: no stdin, no TTY, the output goes to
// stdout and stderr. It returns the exit code of the process (error nil, also
// for a non-zero code) or an error of the stream. ctx ends with the session.
type ExecRunFunc func(ctx context.Context, command []string, stdout, stderr io.Writer) (int, error)

// ExecSummary describes a finished exec session.
type ExecSummary struct {
	// ExitCode is set when the process ended and its exit frame was sent.
	ExitCode    *int
	CloseReason string
	// Result is one of the ExecResult constants.
	Result      string
	Duration    time.Duration
	BytesStdout int64
	BytesStderr int64
	CloseCode   int
	// Started is true when the command ran (the start was audited and the
	// started frame was sent). A session that ended before has no command.
	Started bool
}

// ExecOptions are the inputs of StartExec.
type ExecOptions struct {
	Conn  *gorilla.Conn
	Slot  *Slot // released once when the session ends
	Hub   Registrar
	Run   ExecRunFunc
	OnEnd func(ExecSummary) // called once, from the session goroutine
	// BeforeStart runs once with the checked command, after the start message
	// and before the command. An error vetoes the command (fail closed): the
	// client gets an error frame and the close code 1011. The context ends with
	// the session.
	BeforeStart func(ctx context.Context, command []string) error
	// PostRegister is the re-check after the registration (see
	// PortForwardOptions.PostRegister). It runs before the start message is
	// read.
	PostRegister func(ctx context.Context) bool
	Identity     websocket.ClientIdentity
	InstanceID   string
	Pod          string
	Container    string
	// MaxCommandLength is the largest sum of the argument bytes.
	MaxCommandLength int
	// StartTimeout is the time for the start message (default 10 s).
	StartTimeout time.Duration
}

// ExecSession is one running exec session. It implements websocket.Revocable
// through the embedded Session (Kind() is the kind of its manager, "exec").
type ExecSession struct {
	*Session
	opts        ExecOptions
	exitCode    atomic.Pointer[int]
	bytesStdout atomic.Int64
	bytesStderr atomic.Int64
	started     atomic.Bool
	pumps       sync.WaitGroup
}

var _ websocket.Revocable = (*ExecSession)(nil)

// BytesStdout returns the stdout bytes sent to the client so far.
func (e *ExecSession) BytesStdout() int64 { return e.bytesStdout.Load() }

// BytesStderr returns the stderr bytes sent to the client so far.
func (e *ExecSession) BytesStderr() int64 { return e.bytesStderr.Load() }

// StartExec runs the exec session in its own goroutines and returns at once.
// The session context comes from the manager (not from the request) and ends
// at the maximum duration. The manager should have the kind KindExec.
func (m *Manager) StartExec(o ExecOptions) *ExecSession {
	if o.StartTimeout <= 0 {
		o.StartTimeout = DefaultExecStartTimeout
	}
	ctx, cancel := context.WithTimeout(m.baseCtx, m.cfg.MaxDuration)
	e := &ExecSession{opts: o}
	e.Session = &Session{
		m: m, ws: newWSConn(o.Conn, m.cfg.Timings), slot: o.Slot,
		errDone: make(chan struct{}), ctx: ctx, cancel: cancel, start: time.Now(),
		identity: o.Identity, instance: o.InstanceID,
		onEnd: e.ended,
	}
	e.touch()
	if !m.track() {
		e.finish(CloseGoingAway, "server shutdown")
		e.teardown(nil, nil, false)
		return e
	}
	recordActive(m.cfg.Kind, 1)
	go e.run()
	return e
}

func (e *ExecSession) run() {
	defer e.m.wg.Done()
	defer recoverPanic("exec run")

	var (
		unregister func()
		tornDown   bool
	)
	defer func() {
		if !tornDown {
			e.teardown(unregister, &e.pumps, true)
		}
	}()

	if e.opts.Hub != nil {
		u, err := e.opts.Hub.RegisterRevocable(e)
		if err != nil {
			e.finish(CloseGoingAway, "server shutdown")
		} else {
			unregister = u
		}
	}
	// The re-check comes before the start message is read, so a revoked
	// session never reaches the audit entry or the command.
	if e.opts.PostRegister != nil && e.ctx.Err() == nil && e.postRegisterRevoked(e.opts.PostRegister) {
		e.finish(ClosePolicy, reasonSessionRevoked)
	}

	if e.ctx.Err() == nil {
		e.pumps.Add(1)
		go e.pump(&e.pumps, "exec_flow", e.flow)
	}

	e.supervise()
	tornDown = true
	e.teardown(unregister, &e.pumps, true)
}

type execStartMessage struct {
	Type    string   `json:"type"`
	Command []string `json:"command"`
}

// flow reads the start message, audits, sends started and runs the command.
func (e *ExecSession) flow() {
	command, ok := e.readStart()
	if !ok {
		return
	}
	if e.ctx.Err() != nil {
		return // revoked or shut down meanwhile: no audit, no command
	}
	if e.opts.BeforeStart != nil {
		if err := e.opts.BeforeStart(e.ctx, command); err != nil {
			e.sendError("audit unavailable")
			e.finish(CloseInternalError, "audit unavailable")
			return
		}
	}
	if e.ctx.Err() != nil {
		return
	}
	e.started.Store(true)
	if err := e.sendJSON(map[string]any{"type": "started", "pod": e.opts.Pod, "container": e.opts.Container}); err != nil {
		e.finish(closeAbnormalOnly, "client connection lost")
		return
	}

	// Later client frames are read for close and pong, and ignored.
	e.pumps.Add(1)
	go e.pump(&e.pumps, "exec_drain", e.drainClient)

	code, err := e.opts.Run(e.ctx, command,
		&execFrameWriter{e: e, stream: ExecStreamStdout, n: &e.bytesStdout},
		&execFrameWriter{e: e, stream: ExecStreamStderr, n: &e.bytesStderr})
	if e.ctx.Err() != nil {
		return // the session ended (revoked, shutdown, duration): supervise recorded the code
	}
	switch {
	case err == nil:
		c := code
		if serr := e.sendJSON(map[string]any{"type": "exit", "exit_code": code}); serr != nil {
			e.finish(closeAbnormalOnly, "client connection lost")
			return
		}
		e.exitCode.Store(&c)
		e.finish(CloseNormal, "")
	case errors.Is(err, ErrExecInternal):
		e.sendError("Internal server error")
		e.finish(CloseInternalError, "internal error")
	default:
		e.sendError(truncateMessage(err.Error()))
		e.finish(ClosePodError, "pod stream error")
	}
}

// readStart reads the first client message within the start timeout and checks
// it. On any failure it records the close code and returns false.
func (e *ExecSession) readStart() ([]string, bool) {
	var got atomic.Bool
	timer := time.AfterFunc(e.opts.StartTimeout, func() {
		if !got.Load() {
			e.finish(CloseStartTimeout, "no start message")
		}
	})
	defer timer.Stop()

	mt, data, err := e.ws.read()
	got.Store(true)
	if err != nil {
		e.finishReadError(err)
		return nil, false
	}
	if mt != gorilla.TextMessage {
		e.finish(CloseBadStart, "the start message must be a text frame")
		return nil, false
	}
	var msg execStartMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		e.finish(CloseBadStart, "the start message is not valid JSON")
		return nil, false
	}
	if msg.Type != "start" {
		e.finish(CloseBadStart, `the first message must have type "start"`)
		return nil, false
	}
	if err := ValidateExecCommand(msg.Command, e.maxCommandLength()); err != nil {
		e.finish(CloseBadStart, err.Error())
		return nil, false
	}
	return msg.Command, true
}

func (e *ExecSession) maxCommandLength() int {
	if e.opts.MaxCommandLength > 0 {
		return e.opts.MaxCommandLength
	}
	return 4096
}

func (e *ExecSession) finishReadError(err error) {
	if errors.Is(err, gorilla.ErrReadLimit) {
		e.finish(CloseMessageTooBig, "message too big")
		return
	}
	e.finish(closeAbnormalOnly, "client connection lost")
}

// drainClient reads and ignores the client frames until the connection ends.
func (e *ExecSession) drainClient() {
	for {
		if _, _, err := e.ws.read(); err != nil {
			e.finishReadError(err)
			return
		}
	}
}

func (e *ExecSession) sendJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return e.ws.writeText(b)
}

// sendError sends an error frame (best effort).
func (e *ExecSession) sendError(message string) {
	_ = e.sendJSON(map[string]any{"type": "error", "message": message})
}

func truncateMessage(s string) string {
	s = strings.ToValidUTF8(s, "?")
	if len(s) <= maxExecErrorMessage {
		return s
	}
	cut := maxExecErrorMessage
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// execFrameWriter writes the output of one stream as binary frames: the first
// byte is the stream, the rest raw bytes, at most ExecFrameMax per frame. The
// writes of both streams are serialized by the connection, each has the write
// deadline, and nothing is buffered beyond the frame.
type execFrameWriter struct {
	e      *ExecSession
	n      *atomic.Int64
	stream byte
}

func (w *execFrameWriter) Write(p []byte) (int, error) {
	written := 0
	buf := make([]byte, 0, min(len(p), ExecFrameMax-1)+1)
	for len(p) > 0 {
		if w.e.ctx.Err() != nil {
			return written, w.e.ctx.Err()
		}
		n := min(len(p), ExecFrameMax-1)
		buf = append(buf[:0], w.stream)
		buf = append(buf, p[:n]...)
		if err := w.e.ws.writeBinary(buf); err != nil {
			w.e.finish(closeAbnormalOnly, "client connection lost")
			return written, err
		}
		w.n.Add(int64(n))
		recordBytes(w.e.m.cfg.Kind, DirectionPodToClient, int64(n))
		w.e.touch()
		written += n
		p = p[n:]
	}
	return written, nil
}

// ended builds the summary from the final state and reports it. It runs from
// the teardown, after the connection is closed and the goroutines are done.
func (e *ExecSession) ended(sum Summary) {
	started := e.started.Load()
	es := ExecSummary{
		ExitCode: e.exitCode.Load(), Duration: sum.Duration, CloseCode: sum.CloseCode,
		CloseReason: sum.CloseReason, Started: started,
		BytesStdout: e.bytesStdout.Load(), BytesStderr: e.bytesStderr.Load(),
	}
	es.Result = execResult(sum.CloseCode, es.ExitCode)
	if started {
		switch es.Result {
		case ExecResultZero, ExecResultNonzero, ExecResultError, ExecResultTimeout:
			recordExecExit(es.Result)
		}
	}
	if e.opts.OnEnd != nil {
		e.opts.OnEnd(es)
	}
}

func execResult(closeCode int, exit *int) string {
	switch closeCode {
	case CloseNormal:
		if exit != nil && *exit != 0 {
			return ExecResultNonzero
		}
		return ExecResultZero
	case CloseMaxDuration:
		return ExecResultTimeout
	case ClosePolicy:
		return ExecResultRevoked
	case CloseGoingAway:
		return ExecResultShutdown
	case closeAbnormalOnly:
		return ExecResultClientClosed
	default:
		return ExecResultError
	}
}
