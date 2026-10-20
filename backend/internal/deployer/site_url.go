package deployer

import (
	"context"
	"log/slog"
	"time"

	"backend/internal/k8s"
)

// siteURLStatusTimeout bounds the one namespace status read for the primary
// URL when no readiness wait ran. A slow API server then delays the final
// notification by at most this time; the card has no "Open site" button.
const siteURLStatusTimeout = 5 * time.Second

// primaryURL returns the primary URL of the stack in st: the Ingress with
// the annotation k8s-stack-manager.io/primary-url first, else the first
// Ingress by name (the order of k8s.Client.GetNamespaceStatus, the rule of
// GET /stack-instances/:id/status). An Ingress rule without a host has no
// usable URL. Nil or no Ingress gives "".
func primaryURL(st *k8s.NamespaceStatus) string {
	if st == nil || len(st.Ingresses) == 0 {
		return ""
	}
	ing := st.Ingresses[0]
	if ing.Host == "" {
		return ""
	}
	return ing.URL
}

// siteURLFor returns the primary URL of the stack in namespace for the
// "Open site" button of the channel card (deployment.success,
// deployment.partial, rollback.completed).
//
// When the readiness wait ran (readinessRan), it uses the last status of
// that wait (last, nil when no read succeeded) and reads nothing more.
// Otherwise it reads the namespace status once, with siteURLStatusTimeout. A
// failed read gives "" (no button). Without a notifier or a k8s client it
// reads nothing. The status watcher cache is not used: it runs only on the
// leader.
func (m *Manager) siteURLFor(k8sClient *k8s.Client, namespace string, last *k8s.NamespaceStatus, readinessRan bool) string {
	if m.notifier == nil || k8sClient == nil || namespace == "" {
		return ""
	}
	if readinessRan {
		return primaryURL(last)
	}
	ctx, cancel := context.WithTimeout(m.shutdownCtx, siteURLStatusTimeout)
	defer cancel()
	st, err := k8sClient.GetNamespaceStatus(ctx, namespace, k8s.StatusOptions{})
	if err != nil {
		slog.Warn("could not read the namespace status for the primary URL; the notification has no site URL",
			"namespace", namespace, "error", err)
		return ""
	}
	return primaryURL(st)
}
