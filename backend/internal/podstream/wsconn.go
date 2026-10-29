package podstream

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	gorilla "github.com/gorilla/websocket"
)

// Default timings of a stream WebSocket (see the port-forward contract).
const (
	// DefaultReadLimit is the largest client frame.
	DefaultReadLimit = 64 << 10
	// DefaultReadTimeout is the read deadline, moved on each frame and pong.
	DefaultReadTimeout = 70 * time.Second
	// DefaultWriteTimeout is the deadline of every write.
	DefaultWriteTimeout = 10 * time.Second
	// DefaultPingInterval is the interval of the server pings.
	DefaultPingInterval = 30 * time.Second
	// maxCloseReason is the room for a reason in a close frame (125 bytes of
	// control payload minus the 2 bytes of the code).
	maxCloseReason = 123
)

// Timings are the deadlines of a stream WebSocket. A zero field takes the
// default. Tests inject short values.
type Timings struct {
	ReadLimit    int64
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	PingInterval time.Duration
}

func (t Timings) withDefaults() Timings {
	if t.ReadLimit <= 0 {
		t.ReadLimit = DefaultReadLimit
	}
	if t.ReadTimeout <= 0 {
		t.ReadTimeout = DefaultReadTimeout
	}
	if t.WriteTimeout <= 0 {
		t.WriteTimeout = DefaultWriteTimeout
	}
	if t.PingInterval <= 0 {
		t.PingInterval = DefaultPingInterval
	}
	return t
}

// wsConn wraps a gorilla connection with the deadlines of a long-lived
// stream. Reads come from one goroutine. Data writes come from one goroutine
// too; control frames (ping, close) use WriteControl, which gorilla allows
// concurrently with the data writer.
type wsConn struct {
	c *gorilla.Conn
	t Timings
}

// newWSConn prepares c right after the upgrade. The deadlines of the
// http.Server (ReadTimeout, WriteTimeout) stay on a hijacked connection: they
// are cleared here, and every read and write gets its own deadline.
func newWSConn(c *gorilla.Conn, t Timings) *wsConn {
	t = t.withDefaults()
	w := &wsConn{c: c, t: t}
	if nc := c.NetConn(); nc != nil {
		_ = nc.SetDeadline(time.Time{})
	}
	c.SetReadLimit(t.ReadLimit)
	w.extendRead()
	c.SetPongHandler(func(string) error {
		w.extendRead()
		return nil
	})
	c.SetPingHandler(func(appData string) error {
		w.extendRead()
		err := c.WriteControl(gorilla.PongMessage, []byte(appData), time.Now().Add(t.WriteTimeout))
		if err != nil && !errors.Is(err, gorilla.ErrCloseSent) {
			return err
		}
		return nil
	})
	return w
}

func (w *wsConn) extendRead() {
	_ = w.c.SetReadDeadline(time.Now().Add(w.t.ReadTimeout))
}

// read returns the next data or text frame. Pings, pongs and the deadline
// move are handled inside.
func (w *wsConn) read() (int, []byte, error) {
	mt, data, err := w.c.ReadMessage()
	if err == nil {
		w.extendRead()
	}
	return mt, data, err
}

// writeBinary sends one binary frame with a write deadline. Call it from one
// goroutine only.
func (w *wsConn) writeBinary(p []byte) error {
	_ = w.c.SetWriteDeadline(time.Now().Add(w.t.WriteTimeout))
	return w.c.WriteMessage(gorilla.BinaryMessage, p)
}

func (w *wsConn) ping() error {
	return w.c.WriteControl(gorilla.PingMessage, nil, time.Now().Add(w.t.WriteTimeout))
}

// closeWith sends a close frame with code and reason (best effort) and closes
// the connection.
func (w *wsConn) closeWith(code int, reason string) {
	if code != 1006 { // 1006 must not be sent
		_ = w.c.WriteControl(gorilla.CloseMessage,
			gorilla.FormatCloseMessage(code, TruncateReason(reason)), time.Now().Add(w.t.WriteTimeout))
	}
	_ = w.c.Close()
}

// TruncateReason cuts reason to the 123 bytes that fit in a close frame, at a
// rune boundary, and replaces invalid UTF-8.
func TruncateReason(reason string) string {
	reason = strings.ToValidUTF8(reason, "?")
	if len(reason) <= maxCloseReason {
		return reason
	}
	cut := maxCloseReason
	for cut > 0 && !utf8.RuneStart(reason[cut]) {
		cut--
	}
	return reason[:cut]
}
