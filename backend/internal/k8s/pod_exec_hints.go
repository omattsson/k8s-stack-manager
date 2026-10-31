package k8s

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// maxExecHintTokenReads is the read budget of one ExecContainers call: the
// number of real ServiceAccount and Secret GETs of the service-account-token
// checks (a NotFound counts; a memo hit does not). A container whose check
// needs a read beyond the budget is left out (the hint is conservative; the
// exec route itself has no budget).
const maxExecHintTokenReads = 64

// ExecHintStats tells the caller of ExecContainers how the call went, for
// logging. It holds counts and flags only, no names and no contents.
type ExecHintStats struct {
	// Reads is the number of ServiceAccount and Secret GETs.
	Reads int
	// BudgetCut is true when the read budget left at least one container out.
	BudgetCut bool
	// Forbidden is true when a token check read was refused (the backend
	// identity lacks get on serviceaccounts or secrets).
	Forbidden bool
}

// ExecContainersRequest are the inputs of ExecContainers. The fields mean the
// same as in ExecTargetRequest.
type ExecContainersRequest struct {
	AllowedContainer         func(name string) bool
	AllowServiceAccountToken bool
}

// ReleaseExecContainers are the exec hints of one release.
type ReleaseExecContainers struct {
	// Containers are the names that ResolveExecTarget accepts with a container
	// name, in the order it considers them: pods by name, containers in spec
	// order, first occurrence.
	Containers []string
	// Default is the container that ResolveExecTarget picks without a name. It
	// is empty when that call would be refused.
	Default string
}

// ExecContainers tells, for each release, which containers ResolveExecTarget
// would accept. It lists the pods of the namespace once and groups them by
// release in memory (a pod belongs to a release when app.kubernetes.io/instance
// or helm.sh/release is the release name, like releasePods). The pod and
// container choice is the one of ResolveExecTarget (pickExecContainer). Unless
// AllowServiceAccountToken, a container is listed only when the token check
// says it holds no token; a failed check (Forbidden included) leaves it out.
// Each ServiceAccount and Secret is read at most once, and at most
// maxExecHintTokenReads reads are made per call; when the budget is spent the
// remaining containers that need a read are left out (stats.BudgetCut). A
// release without an accepted container is not in the result.
func (c *Client) ExecContainers(ctx context.Context, namespace string, releases []string, req ExecContainersRequest) (out map[string]ReleaseExecContainers, stats ExecHintStats, err error) {
	ctx, span := startSpan(ctx, "k8s.ExecContainers", attribute.String("k8s.namespace", namespace))
	defer func() { endSpan(span, err) }()

	out = map[string]ReleaseExecContainers{}
	if len(releases) == 0 || req.AllowedContainer == nil {
		return out, stats, nil
	}
	list, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, stats, fmt.Errorf("list pods in %s: %w", namespace, err)
	}

	byRelease := map[string][]corev1.Pod{}
	for _, r := range releases {
		byRelease[r] = nil
	}
	for i := range list.Items {
		p := &list.Items[i]
		for _, key := range []string{releaseLabelInstance, releaseLabelHelm} {
			r, ok := p.Labels[key]
			if !ok {
				continue
			}
			if _, want := byRelease[r]; !want {
				continue
			}
			byRelease[r] = append(byRelease[r], *p)
			if key == releaseLabelInstance && p.Labels[releaseLabelHelm] == r {
				break // both labels name the release: one entry
			}
		}
	}

	memo := newTokenMemo()
	memo.maxReads = maxExecHintTokenReads
	processed := map[string]bool{}
	for _, release := range releases {
		if processed[release] {
			continue
		}
		processed[release] = true
		ready := readySortedPods(byRelease[release])
		var res ReleaseExecContainers
		defName := ""
		if p, cc := pickExecContainer(ready, "", req.AllowedContainer); p != nil {
			defName = cc.Name
		}
		seen := map[string]bool{}
		for i := range ready {
			for j := range ready[i].Spec.Containers {
				name := ready[i].Spec.Containers[j].Name
				if seen[name] || !req.AllowedContainer(name) {
					continue
				}
				seen[name] = true
				pod, ctr := pickExecContainer(ready, name, req.AllowedContainer)
				if !req.AllowServiceAccountToken {
					holds, err := c.holdsServiceAccountToken(ctx, namespace, pod, ctr, memo)
					if err != nil && ctx.Err() != nil {
						stats.Reads = memo.reads
						return nil, stats, ctx.Err()
					}
					if err != nil {
						switch {
						case errors.Is(err, errTokenReadBudget):
							stats.BudgetCut = true
						case errors.Is(err, ErrTokenCheckForbidden):
							stats.Forbidden = true
						}
						continue
					}
					if holds {
						continue
					}
				}
				res.Containers = append(res.Containers, name)
				if name == defName {
					res.Default = name
				}
			}
		}
		if len(res.Containers) > 0 {
			out[release] = res
		}
	}
	stats.Reads = memo.reads
	return out, stats, nil
}
