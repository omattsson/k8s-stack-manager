package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/ctxutil"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/internal/podstream"

	"github.com/gin-gonic/gin"
)

const (
	// execHintTimeout bounds one computation of the exec hints (the flight,
	// which runs detached from the callers).
	execHintTimeout = 10 * time.Second
	// execHintWait bounds how long one caller waits for the flight. After it
	// the caller gets exec_hints=true without lists; the flight goes on and a
	// later call joins it.
	execHintWait = 3 * time.Second
)

// ExecHintProvider computes the exec hints of a stack instance for the caller
// of the request. *StreamHandler implements it; InstanceHandler uses it for
// /status and /pods with exec_hints=true.
type ExecHintProvider interface {
	// ExecHints returns ok=false when the caller gets no hints at all (the
	// caller may not modify the instance, exec is off, or the cluster does not
	// allow it, or it could not be checked). With ok=true the hints are by
	// release name; a release without an entry has no usable container, and a
	// nil map means the hints could not be computed in time. err is set only
	// when the request context ended (cancelled or past its deadline). The
	// returned map is shared between callers: do not change it.
	ExecHints(c *gin.Context, inst *models.StackInstance) (hints map[string]k8s.ReleaseExecContainers, ok bool, err error)
}

var _ ExecHintProvider = (*StreamHandler)(nil)

// ExecHints implements ExecHintProvider. It runs the checks of the exec route
// that depend on the caller (feature switch, role, cluster switch) first; only
// then the shared computation runs, which is the same for every authorized
// caller. It writes no response.
func (h *StreamHandler) ExecHints(c *gin.Context, inst *models.StackInstance) (map[string]k8s.ReleaseExecContainers, bool, error) {
	if inst == nil || h.clusterRepo == nil || h.instanceRepo == nil || !h.cfg.Exec.Enabled || !canModifyInstance(c, inst) {
		return nil, false, nil
	}
	// The exec route refuses an API key unless it is allowed.
	if middleware.GetAuthMethodFromContext(c) == middleware.AuthMethodAPIKey && !h.cfg.AllowAPIKeys {
		return nil, false, nil
	}
	ctx := c.Request.Context()
	cl, err := h.findCluster(ctx, inst.ClusterID)
	if err != nil {
		if _, _, cancelled := cancelledRequestError(err); cancelled {
			return nil, false, err
		}
		if status, _ := mapError(err, entityCluster); status == http.StatusNotFound {
			return nil, false, nil // no such cluster: nothing allows exec
		}
		// Fail closed: the cluster must allow exec, and this one cannot say.
		slog.Warn("Exec hints: cluster lookup failed", "instance_id", inst.ID, "cluster_id", inst.ClusterID, "error", err)
		return nil, false, nil
	}
	if cl == nil || !clusterAllowsStream(cl, podstream.KindExec) {
		return nil, false, nil
	}

	// Authorized. A stack that cannot exec has no usable container.
	if len(h.cfg.Exec.AllowedContainers) == 0 || !streamInstanceLive(inst) {
		return nil, true, nil
	}
	hints, err := h.sharedExecHints(ctx, inst)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		slog.Warn("Exec hints: not computed", "instance_id", inst.ID, "error", err)
		return nil, true, nil
	}
	return hints, true, nil
}

// sharedExecHints lets concurrent calls for one instance share one
// computation. The result does not depend on the caller, which the caller of
// this method has authorized already. It is not cached after the calls end: a
// stack that just became running must show its new pods at the next read.
//
// Cost of one computation: the database (namespace owner, charts) plus one
// namespace GET, one pod list and one GET for each distinct ServiceAccount and
// Secret of the token check (at most k8s.maxExecHintTokenReads GETs). The
// computation runs detached from the callers for at most execHintTimeout; a
// caller waits at most h.hintWait (execHintWait) and then gets no lists.
func (h *StreamHandler) sharedExecHints(ctx context.Context, inst *models.StackInstance) (map[string]k8s.ReleaseExecContainers, error) {
	key := fmt.Sprintf("%s/%s/%s/%s", inst.ID, inst.ClusterID, inst.Namespace, inst.StackDefinitionID)
	ch := h.hintFlight.DoChan(key, func() (_ interface{}, err error) {
		// DoChan re-panics in a new goroutine when callers wait: turn a panic
		// into an error.
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("exec hints panic: %v", r)
			}
		}()
		// The first caller leaving must not fail the others.
		readCtx, cancel := ctxutil.Detach(ctx, execHintTimeout)
		defer cancel()
		return h.computeExecHints(readCtx, inst)
	})
	if h.hintJoined != nil {
		h.hintJoined()
	}
	wait := h.hintWait
	if wait <= 0 {
		wait = execHintWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		// The flight goes on; a later call joins it.
		slog.Debug("Exec hints: wait timed out", "instance_id", inst.ID, "wait", wait)
		return nil, nil
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(map[string]k8s.ReleaseExecContainers), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// computeExecHints runs the checks 8 and 9 of the exec route and asks the
// cluster client which containers it would accept. A failed check gives an
// error, so the hints stay empty.
func (h *StreamHandler) computeExecHints(ctx context.Context, inst *models.StackInstance) (map[string]k8s.ReleaseExecContainers, error) {
	if h.execProvider == nil || h.chartRepo == nil {
		return nil, errors.New("exec is not wired")
	}
	shared, err := h.instanceRepo.NamespaceShared(ctx, inst.Namespace, inst.ID)
	if err != nil {
		return nil, fmt.Errorf("namespace owner lookup: %w", err)
	}
	if shared {
		return map[string]k8s.ReleaseExecContainers{}, nil
	}
	ec, err := h.execProvider(inst.ClusterID)
	if err != nil || ec == nil {
		return nil, fmt.Errorf("exec client: %v", err)
	}
	if err := ec.CheckStreamNamespace(ctx, inst.Namespace, h.cfg.BackendNamespace); err != nil {
		return nil, fmt.Errorf("check namespace: %w", err)
	}
	charts, err := h.chartRepo.ListByDefinition(ctx, inst.StackDefinitionID)
	if err != nil {
		return nil, fmt.Errorf("chart lookup: %w", err)
	}
	// Only charts that the route accepts by name get a hint.
	releases := make([]string, 0, len(charts))
	for i := range charts {
		if validExecChartName(charts[i].ChartName) {
			releases = append(releases, charts[i].ChartName)
		}
	}
	hints, stats, err := ec.ExecContainers(ctx, inst.Namespace, releases, k8s.ExecContainersRequest{
		AllowedContainer:         h.cfg.Exec.ContainerAllowed,
		AllowServiceAccountToken: h.cfg.Exec.AllowServiceAccountToken,
	})
	if err != nil {
		return nil, fmt.Errorf("exec containers: %w", err)
	}
	// Names and counts only; never secret contents.
	if stats.Forbidden {
		slog.Warn("Exec hints: the backend may not read service accounts or secrets; containers left out (grant get on serviceaccounts and secrets)",
			"instance_id", inst.ID, "cluster_id", inst.ClusterID, "namespace", inst.Namespace)
	}
	if stats.BudgetCut {
		slog.Debug("Exec hints: read budget spent; containers left out",
			"instance_id", inst.ID, "namespace", inst.Namespace, "reads", stats.Reads)
	}
	// Only the charts of the definition get hints (the client answers for the
	// releases asked; this keeps it so).
	out := make(map[string]k8s.ReleaseExecContainers, len(hints))
	for _, r := range releases {
		if hint, ok := hints[r]; ok {
			out[r] = hint
		}
	}
	return out, nil
}

// execHintsRequested reports whether the query asks for exec hints
// (exec_hints=true; anything that is not a true boolean is off).
func execHintsRequested(c *gin.Context) bool {
	v, err := strconv.ParseBool(c.Query("exec_hints"))
	return err == nil && v
}

// withExecHints returns the status to send. Without exec_hints=true, or when
// the caller gets no hints, that is ns itself and nothing else is read. Else it
// is a copy of ns with the exec fields. ns is never changed: the watcher cache
// and the shared status read hand the same object to every caller. It returns
// false when it wrote the 499 answer of a cancelled request; a failed hint
// computation never fails the response.
func (h *InstanceHandler) withExecHints(c *gin.Context, inst *models.StackInstance, ns *k8s.NamespaceStatus) (*k8s.NamespaceStatus, bool) {
	if h.execHints == nil || ns == nil || !execHintsRequested(c) {
		return ns, true
	}
	hints, ok, err := h.execHints.ExecHints(c, inst)
	if err != nil {
		if respondCancelled(c, err) {
			return nil, false
		}
		slog.Warn("Exec hints: not computed", logKeyInstanceID, inst.ID, "error", err)
		return ns, true
	}
	if !ok {
		return ns, true
	}
	out := *ns
	out.ExecHints = true
	out.Charts = make([]k8s.ChartStatus, len(ns.Charts))
	copy(out.Charts, ns.Charts)
	for i := range out.Charts {
		hint, found := hints[out.Charts[i].ReleaseName]
		if !found || len(hint.Containers) == 0 {
			continue
		}
		out.Charts[i].ExecContainers = slices.Clone(hint.Containers)
		out.Charts[i].ExecDefaultContainer = hint.Default
	}
	return &out, true
}
