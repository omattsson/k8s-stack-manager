package deployer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"backend/internal/k8s"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	// defaultPodEventPollInterval is how often a running deploy, rollback or
	// stabilize phase reads the Warning events and pod states of its
	// namespace.
	defaultPodEventPollInterval = 10 * time.Second
	// podEventLookback is subtracted from the start time of the watch, so
	// a small clock difference to the cluster does not hide new events.
	podEventLookback = 10 * time.Second
	// maxPodEventLinesPerPoll limits the new lines of one poll.
	maxPodEventLinesPerPoll = 5
	// maxPodEventLines limits the lines of one operation. After the limit
	// one notice line follows and new problems are only kept for the error
	// message.
	maxPodEventLines = 30
	// maxPodEventMessageLen limits the message part of one line.
	maxPodEventMessageLen = 300
)

// podProblemLister reads the pod problems of a namespace. *k8s.Client
// implements it.
type podProblemLister interface {
	ListPodProblems(ctx context.Context, namespace string, since time.Time) ([]k8s.PodProblem, error)
}

// podEventWatch polls the Warning events and container states of a namespace
// while Helm installs the charts and while the pods stabilize. It writes
// each new problem once (de-duplicated, rate-limited) to the WebSocket log
// and collects the lines for the stored deploy log output. It also keeps
// the most relevant problem for the error message of a failed operation.
//
// Problems with the same reason and object form one group, for example the
// FailedCreate events of one ReplicaSet for each pod that the quota rejects
// (each event names another pod). A group gives one line with the number of
// problems: "Pod event: FailedCreate replicaset/db (x18): <latest message>".
// The WebSocket log shows the group when it starts and again each time its
// count doubles (the stream cannot change a line it sent). The stored log
// shows the group once per drain with the current count.
type podEventWatch struct {
	lister    podProblemLister
	namespace string
	since     time.Time
	emit      func(line string)

	mu        sync.Mutex
	seen      map[string]struct{}
	groups    map[string]*podProblemGroup
	pending   []pendingPodLine // lines not yet taken by drain
	emitted   int              // groups shown (the line cap counts groups)
	capped    bool
	best      string // most relevant problem, as one line
	bestScore int

	// forbiddenLogged is true after the first Forbidden error: one warning
	// per operation, not one per poll.
	forbiddenLogged bool

	cancel context.CancelFunc
	done   chan struct{}
}

// podProblemGroup is the problems of one reason and object.
type podProblemGroup struct {
	reason   string
	object   string
	message  string // message of the latest problem
	count    int    // distinct problems in the group
	streamed int    // count of the last streamed line
}

// pendingPodLine is one line for the stored log: a group (formatted with its
// count at drain time) or a fixed text.
type pendingPodLine struct {
	group string // group key; empty for a fixed text
	text  string
}

// podProblemGroupKey returns the group of a problem: its reason and object.
func podProblemGroupKey(p k8s.PodProblem) string {
	return p.Reason + "|" + p.Object
}

// startPodEventWatch starts a podEventWatch for the namespace. emit gets
// each new line (for the WebSocket log). It returns nil when lister is nil.
// Call stop when the operation no longer waits for pods.
func (m *Manager) startPodEventWatch(lister podProblemLister, namespace string, emit func(line string)) *podEventWatch {
	if lister == nil || namespace == "" {
		return nil
	}
	interval := m.podEventPollInterval
	if interval <= 0 {
		interval = defaultPodEventPollInterval
	}
	ctx, cancel := context.WithCancel(m.shutdownCtx)
	w := &podEventWatch{
		lister:    lister,
		namespace: namespace,
		since:     time.Now().UTC().Add(-podEventLookback),
		emit:      emit,
		seen:      map[string]struct{}{},
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	go w.run(ctx, interval)
	return w
}

// run polls until ctx is done.
func (w *podEventWatch) run(ctx context.Context, interval time.Duration) {
	defer close(w.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

// poll reads the problems once and records the new ones.
func (w *podEventWatch) poll(ctx context.Context) {
	pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	problems, err := w.lister.ListPodProblems(pollCtx, w.namespace, w.since)
	if err != nil {
		w.logPollError(err)
		return
	}
	w.record(problems)
}

// logPollError logs a failed poll: a missing permission once per operation
// as a warning (the service account needs "list" on events and pods),
// other errors at debug level.
func (w *podEventWatch) logPollError(err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if apierrors.IsForbidden(err) {
		w.mu.Lock()
		first := !w.forbiddenLogged
		w.forbiddenLogged = true
		w.mu.Unlock()
		if first {
			slog.Warn("pod events are not shown in the deploy log: the service account may not list events or pods",
				"namespace", w.namespace, "error", err)
		}
		return
	}
	slog.Debug("pod event poll failed", "namespace", w.namespace, "error", err)
}

// record adds the new problems: it de-duplicates them, groups them by
// reason and object, keeps the most relevant one and shows at most
// maxPodEventLinesPerPoll new groups per poll (and at most maxPodEventLines
// groups for the operation). A new problem of a known group only raises the
// count of the group.
func (w *podEventWatch) record(problems []k8s.PodProblem) {
	var lines []string
	w.mu.Lock()
	if w.groups == nil {
		w.groups = map[string]*podProblemGroup{}
	}
	perPoll := 0
	var touched []*podProblemGroup // groups changed by this poll, in order
	isNew := map[*podProblemGroup]bool{}
	inTouched := map[*podProblemGroup]bool{}
	touch := func(g *podProblemGroup) {
		if !inTouched[g] {
			inTouched[g] = true
			touched = append(touched, g)
		}
	}
	var notices []string
	for _, p := range problems {
		if _, dup := w.seen[p.Key]; dup {
			continue
		}
		gk := podProblemGroupKey(p)
		if g := w.groups[gk]; g != nil {
			w.seen[p.Key] = struct{}{}
			w.updateBest(p)
			g.count++
			g.message = p.Message
			touch(g)
			w.pending = append(w.pending, pendingPodLine{group: gk})
			continue
		}
		if w.capped {
			w.seen[p.Key] = struct{}{}
			w.updateBest(p)
			continue
		}
		if w.emitted >= maxPodEventLines {
			w.seen[p.Key] = struct{}{}
			w.updateBest(p)
			w.capped = true
			const notice = "Pod events: more problems are not shown"
			notices = append(notices, notice)
			w.pending = append(w.pending, pendingPodLine{text: notice})
			continue
		}
		if perPoll >= maxPodEventLinesPerPoll {
			// Leave the key unseen, so the next poll shows it.
			continue
		}
		w.seen[p.Key] = struct{}{}
		w.updateBest(p)
		perPoll++
		w.emitted++
		g := &podProblemGroup{reason: p.Reason, object: p.Object, message: p.Message, count: 1}
		w.groups[gk] = g
		isNew[g] = true
		touch(g)
		w.pending = append(w.pending, pendingPodLine{group: gk})
	}
	// Stream a group of this poll with its count. Stream a known group again
	// only when its count has doubled since its last streamed line, so a
	// long run of rejected pods gives few lines.
	for _, g := range touched {
		if isNew[g] || g.count >= 2*g.streamed {
			g.streamed = g.count
			lines = append(lines, "Pod event: "+g.line())
		}
	}
	lines = append(lines, notices...)
	w.mu.Unlock()

	if w.emit != nil {
		for _, l := range lines {
			w.emit(l)
		}
	}
}

// updateBest keeps p as the most relevant problem when its score is at
// least the current one. Caller must hold w.mu.
func (w *podEventWatch) updateBest(p k8s.PodProblem) {
	if score := podProblemScore(p); score >= w.bestScore {
		w.best = formatPodProblem(p)
		w.bestScore = score
	}
}

// line returns the line of the group: one problem as formatPodProblem, more
// problems with the count after the object.
func (g *podProblemGroup) line() string {
	p := k8s.PodProblem{Reason: g.reason, Object: g.object, Message: g.message}
	if g.count > 1 {
		p.Object = fmt.Sprintf("%s (x%d)", g.object, g.count)
	}
	return formatPodProblem(p)
}

// drain returns the lines recorded since the last drain, one per line with
// a trailing newline, for the stored deploy log output. A group appears once
// per drain, with its current count. A nil watch returns "".
func (w *podEventWatch) drain() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) == 0 {
		return ""
	}
	var b strings.Builder
	written := make(map[string]bool, len(w.pending))
	for _, pl := range w.pending {
		if pl.group == "" {
			b.WriteString(pl.text + "\n")
			continue
		}
		if written[pl.group] {
			continue
		}
		written[pl.group] = true
		if g := w.groups[pl.group]; g != nil {
			b.WriteString("Pod event: " + g.line() + "\n")
		}
	}
	w.pending = nil
	return b.String()
}

// stop ends the polling, makes one last poll, and returns the remaining
// lines (as drain) and the most relevant problem ("" when there was none).
// A nil watch returns "", "".
func (w *podEventWatch) stop() (string, string) {
	if w == nil {
		return "", ""
	}
	w.cancel()
	<-w.done
	// One last read: a problem of the last seconds (for example the quota
	// rejection just before the Helm hook timed out) is not lost.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if problems, err := w.lister.ListPodProblems(ctx, w.namespace, w.since); err == nil {
		w.record(problems)
	} else {
		w.logPollError(err)
	}
	cancel()
	w.mu.Lock()
	best := w.best
	w.mu.Unlock()
	return w.drain(), best
}

// formatPodProblem returns one concise line for the problem.
func formatPodProblem(p k8s.PodProblem) string {
	msg := strings.Join(strings.Fields(p.Message), " ")
	if len(msg) > maxPodEventMessageLen {
		msg = msg[:maxPodEventMessageLen] + "..."
	}
	if msg == "" {
		return fmt.Sprintf("%s %s", p.Reason, p.Object)
	}
	return fmt.Sprintf("%s %s: %s", p.Reason, p.Object, msg)
}

// podProblemScore ranks problems for the error message: a quota or
// LimitRange rejection first, then a pod that cannot be created or
// scheduled, then a container that does not start, then the rest. A later
// problem with the same score replaces an earlier one.
func podProblemScore(p k8s.PodProblem) int {
	switch {
	case k8s.IsQuotaProblem(p.Message):
		return 4
	case p.Reason == "FailedCreate" || p.Reason == "FailedScheduling":
		return 3
	case p.Reason == "ImagePullBackOff" || p.Reason == "ErrImagePull" || p.Reason == "CrashLoopBackOff" ||
		p.Reason == "InvalidImageName" || p.Reason == "CreateContainerConfigError" || p.Reason == "CreateContainerError":
		return 2
	default:
		return 1
	}
}

// podEventError adds the most relevant pod problem to an operation error,
// so the instance error message names the cause (for example the exceeded
// ResourceQuota) and not only the timeout.
type podEventError struct {
	err   error
	event string
}

func (e *podEventError) Error() string {
	return e.err.Error() + " (pod event: " + e.event + ")"
}

func (e *podEventError) Unwrap() error { return e.err }

// withPodEvent returns err with the pod problem added. It returns err
// unchanged when err is nil or event is empty.
func withPodEvent(err error, event string) error {
	if err == nil || event == "" {
		return err
	}
	return &podEventError{err: err, event: event}
}

// podProblemListerFor returns the k8s client as a podProblemLister, or nil
// for a nil client (a typed nil in the interface would not compare to nil).
func podProblemListerFor(c *k8s.Client) podProblemLister {
	if c == nil {
		return nil
	}
	return c
}
