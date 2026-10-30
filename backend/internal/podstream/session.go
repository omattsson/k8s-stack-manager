package podstream

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/k8s"
	"backend/internal/websocket"

	gorilla "github.com/gorilla/websocket"
)

// errGrace is how long a pod EOF waits for the text of the error stream.
const errGrace = 250 * time.Millisecond

// Registrar registers a session for revocation. *websocket.Hub implements it.
type Registrar interface {
	RegisterRevocable(r websocket.Revocable) (unregister func(), err error)
}

// Summary describes a finished session.
type Summary struct {
	CloseReason string
	Duration    time.Duration
	BytesIn     int64 // client to pod
	BytesOut    int64 // pod to client
	CloseCode   int
}

// PortForwardOptions are the inputs of StartPortForward.
type PortForwardOptions struct {
	Conn     *gorilla.Conn
	Stream   k8s.PodPortStream
	Slot     *Slot // released once when the session ends
	Hub      Registrar
	OnEnd    func(Summary) // called once, from the session goroutine
	Identity websocket.ClientIdentity
	// InstanceID is the stack instance of the tunnel.
	InstanceID string
	// PostRegister runs once, right after the hub registered the session and
	// before any byte is copied. It re-checks what could have changed between
	// the authentication and the registration (a logout, a user revoke or a
	// role change in that window missed this session). It returns true when
	// the session is revoked: the session then ends with 1008 "session
	// revoked". The context ends with the session and after postRegisterTimeout.
	// The implementation must fail open on a lookup error (return false).
	PostRegister func(ctx context.Context) bool
}

// postRegisterTimeout bounds PostRegister.
const postRegisterTimeout = 5 * time.Second

// reasonSessionRevoked is the close reason of a session that PostRegister
// found revoked.
const reasonSessionRevoked = "session revoked"

// Session is one running tunnel. It implements websocket.Revocable.
type Session struct {
	m        *Manager
	ws       *wsConn
	stream   k8s.PodPortStream
	slot     *Slot
	onEnd    func(Summary)
	ctx      context.Context
	cancel   context.CancelFunc
	start    time.Time
	identity websocket.ClientIdentity
	instance string

	reasonText string
	bytesIn    atomic.Int64
	bytesOut   atomic.Int64
	// lastActivity is the UnixNano of the last data frame.
	lastActivity atomic.Int64
	// errDone is closed when the error stream reader has ended.
	errDone    chan struct{}
	resultMu   sync.Mutex
	resultCode int
	resultSet  bool
}

var _ websocket.Revocable = (*Session)(nil)

// Identity implements websocket.Revocable.
func (s *Session) Identity() websocket.ClientIdentity { return s.identity }

// InstanceID implements websocket.Revocable.
func (s *Session) InstanceID() string { return s.instance }

// Kind implements websocket.Revocable.
func (s *Session) Kind() string {
	if s.m == nil {
		return KindPortForward
	}
	return s.m.cfg.Kind
}

// Revoke implements websocket.Revocable. It records the close and returns; the
// session goroutine does the close.
func (s *Session) Revoke(code int, reason string) { s.finish(code, reason) }

// BytesIn returns the bytes copied from the client to the pod so far.
func (s *Session) BytesIn() int64 { return s.bytesIn.Load() }

// BytesOut returns the bytes copied from the pod to the client so far.
func (s *Session) BytesOut() int64 { return s.bytesOut.Load() }

// finish records the first close result and ends the session context. It does
// not block.
func (s *Session) finish(code int, reason string) {
	s.resultMu.Lock()
	if !s.resultSet {
		s.resultSet = true
		s.resultCode = code
		s.reasonText = reason
	}
	s.resultMu.Unlock()
	s.cancel()
}

func (s *Session) result() (int, string) {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	return s.resultCode, s.reasonText
}

func (s *Session) touch() { s.lastActivity.Store(time.Now().UnixNano()) }

// StartPortForward runs the tunnel between the WebSocket and the pod stream in
// its own goroutines and returns at once. The session context comes from the
// manager (not from the request) and ends at the maximum duration. On any
// start failure the session ends with a close code and the slot is released.
func (m *Manager) StartPortForward(o PortForwardOptions) *Session {
	ctx, cancel := context.WithTimeout(m.baseCtx, m.cfg.MaxDuration)
	s := &Session{
		m: m, ws: newWSConn(o.Conn, m.cfg.Timings), stream: o.Stream, slot: o.Slot, onEnd: o.OnEnd,
		errDone: make(chan struct{}), ctx: ctx, cancel: cancel, start: time.Now(), identity: o.Identity, instance: o.InstanceID,
	}
	s.touch()
	if !m.track() {
		s.finish(CloseGoingAway, "server shutdown")
		s.teardown(nil, nil, false)
		return s
	}
	recordActive(m.cfg.Kind, 1)
	go s.run(o.Hub, o.PostRegister)
	return s
}

func (s *Session) run(hub Registrar, postRegister func(context.Context) bool) {
	defer s.m.wg.Done()
	defer recoverPanic("session run")

	var (
		unregister func()
		pumps      sync.WaitGroup
		tornDown   bool
	)
	// The teardown always runs, also after a panic below: it closes the
	// connection and the pod stream, releases the slot and the hub entry and
	// reports the end. A panic leaves no recorded result: 1011.
	defer func() {
		if !tornDown {
			s.teardown(unregister, &pumps, true)
		}
	}()

	if hub != nil {
		u, err := hub.RegisterRevocable(s)
		if err != nil {
			s.finish(CloseGoingAway, "server shutdown")
		} else {
			unregister = u
		}
	}
	if postRegister != nil && s.ctx.Err() == nil && s.postRegisterRevoked(postRegister) {
		s.finish(ClosePolicy, reasonSessionRevoked)
	}

	pumps.Add(3)
	go s.pump(&pumps, "client_to_pod", s.clientToPod)
	go s.pump(&pumps, "pod_to_client", s.podToClient)
	go s.pump(&pumps, "pod_errors", s.podErrors)

	s.supervise()
	tornDown = true
	s.teardown(unregister, &pumps, true)
}

// postRegisterRevoked runs the check of the options with a bound.
func (s *Session) postRegisterRevoked(check func(context.Context) bool) bool {
	ctx, cancel := context.WithTimeout(s.ctx, postRegisterTimeout)
	defer cancel()
	return check(ctx)
}

// supervise runs the pings, the idle timer and the maximum duration until the
// session context ends.
func (s *Session) supervise() {
	defer recoverPanic("session supervise")
	ping := time.NewTicker(s.m.cfg.Timings.PingInterval)
	defer ping.Stop()
	idle := s.m.cfg.IdleTimeout
	var idleTimer *time.Timer
	var idleC <-chan time.Time // nil: no idle timeout
	if !s.m.cfg.NoIdleTimeout {
		idleTimer = time.NewTimer(idle)
		idleC = idleTimer.C
		defer idleTimer.Stop()
	}
	for {
		select {
		case <-s.ctx.Done():
			// finish() records the code first; without a record the context
			// ended by itself.
			if _, set := s.recorded(); !set {
				if errors.Is(s.ctx.Err(), context.DeadlineExceeded) {
					s.finish(CloseMaxDuration, "maximum duration reached")
				} else {
					s.finish(CloseGoingAway, "server shutdown")
				}
			}
			return
		case <-ping.C:
			if err := s.ws.ping(); err != nil {
				s.finish(closeAbnormalOnly, "ping failed")
				return
			}
		case <-idleC:
			since := time.Since(time.Unix(0, s.lastActivity.Load()))
			if since >= idle {
				s.finish(CloseIdleTimeout, "idle timeout")
				return
			}
			idleTimer.Reset(idle - since)
		}
	}
}

func (s *Session) recorded() (int, bool) {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	return s.resultCode, s.resultSet
}

func (s *Session) pump(wg *sync.WaitGroup, name string, fn func()) {
	defer wg.Done()
	defer func() {
		if p := recover(); p != nil {
			slog.Error("Panic in pod stream pump", "pump", name, "recover", p)
			s.finish(1011, "internal error")
		}
	}()
	fn()
}

// clientToPod copies binary frames to the pod. A text frame closes with 1003.
func (s *Session) clientToPod() {
	for {
		mt, data, err := s.ws.read()
		if err != nil {
			var ce *gorilla.CloseError
			if errors.Is(err, gorilla.ErrReadLimit) {
				s.finish(CloseMessageTooBig, "message too big")
			} else if errors.As(err, &ce) {
				s.finish(CloseNormal, "")
			} else {
				s.finish(closeAbnormalOnly, "client connection lost")
			}
			return
		}
		if mt != gorilla.BinaryMessage {
			s.finish(CloseUnsupported, "text frames are not supported")
			return
		}
		if len(data) == 0 {
			continue
		}
		if _, err := s.stream.Data().Write(data); err != nil {
			s.finish(ClosePodError, "write to pod failed: "+err.Error())
			return
		}
		s.bytesIn.Add(int64(len(data)))
		recordBytes(s.m.cfg.Kind, DirectionClientToPod, int64(len(data)))
		s.touch()
	}
}

// podToClient copies the pod bytes to the client in binary frames. EOF ends
// the tunnel normally.
func (s *Session) podToClient() {
	buf := make([]byte, 32<<10)
	rd := s.stream.Data()
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			if werr := s.ws.writeBinary(buf[:n]); werr != nil {
				s.finish(closeAbnormalOnly, "client connection lost")
				return
			}
			s.bytesOut.Add(int64(n))
			recordBytes(s.m.cfg.Kind, DirectionPodToClient, int64(n))
			s.touch()
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// The kubelet may send the error text just before the
				// data stream ends: give the error reader a moment.
				select {
				case <-s.errDone:
				case <-time.After(errGrace):
				}
				s.finish(CloseNormal, "")
			} else {
				s.finish(ClosePodError, err.Error())
			}
			return
		}
	}
}

// podErrors ends the tunnel with 4502 when the error stream of the pod yields
// text.
func (s *Session) podErrors() {
	defer close(s.errDone)
	r := s.stream.Errors()
	if r == nil {
		return
	}
	buf := make([]byte, 4<<10)
	n, err := r.Read(buf)
	if n > 0 {
		s.finish(ClosePodError, string(buf[:n]))
		return
	}
	_ = err // EOF without text: the forward ended; podToClient reports it
}

// teardown closes the connection and the stream, waits for the pumps,
// unregisters, and reports the summary.
func (s *Session) teardown(unregister func(), pumps *sync.WaitGroup, counted bool) {
	defer recoverPanic("session teardown")
	defer s.slot.Release()
	code, reason := s.result()
	if _, set := s.recorded(); !set {
		// Nothing recorded a result (a panic before the end): never code 0.
		code, reason = CloseInternalError, "internal error"
	}
	if unregister != nil {
		unregister()
	}
	s.cancel()
	s.ws.closeWith(code, reason)
	if s.stream != nil {
		_ = s.stream.Close()
	}
	if pumps != nil {
		waitTimeout(pumps, 5*time.Second)
	}
	sum := Summary{
		BytesIn: s.bytesIn.Load(), BytesOut: s.bytesOut.Load(),
		Duration: time.Since(s.start), CloseCode: code, CloseReason: TruncateReason(reason),
	}
	if counted {
		recordActive(s.m.cfg.Kind, -1)
	}
	recordClosed(s.m.cfg.Kind, closeReasonLabel(code))
	if s.onEnd != nil {
		s.onEnd(sum)
	}
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		slog.Warn("Pod stream pumps did not stop in time")
	}
}

func recoverPanic(where string) {
	if p := recover(); p != nil {
		slog.Error("Panic in pod stream", "where", where, "recover", p)
	}
}
