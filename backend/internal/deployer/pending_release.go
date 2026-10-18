package deployer

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// pendingReleaseStatuses are the helm release states that block every later
// install, upgrade or rollback with "another operation is in progress".
var pendingReleaseStatuses = map[string]bool{
	"pending-install":  true,
	"pending-upgrade":  true,
	"pending-rollback": true,
}

// helmReleaseSecretName returns the name of the Secret in which the helm
// "secret" storage driver (the default) keeps one release revision.
func helmReleaseSecretName(releaseName string, revision int) string {
	return "sh.helm.release.v1." + releaseName + ".v" + strconv.Itoa(revision)
}

// recoverPendingRelease unblocks a release whose latest revision is stuck in
// pending-*, for example after the helm process was killed during a deploy.
// It deletes the Secret of that one revision, so the next helm upgrade
// --install continues from the previous revision (or installs again and adopts
// the existing resources for a stuck pending-install). It never runs helm
// uninstall, so PVCs and their data stay.
//
// The caller must make sure that no other helm operation runs for the release:
// the Manager serializes operations per instance through the instance status.
//
// It returns a message for the deploy log when it recovered a release, and ""
// when there was nothing to do. A lookup failure is not an error: the release
// may not exist yet, and helm reports a real problem itself.
func recoverPendingRelease(ctx context.Context, helm HelmExecutor, cs kubernetes.Interface, releaseName, namespace string) (string, error) {
	if cs == nil {
		return "", nil
	}
	revisions, err := helm.History(ctx, releaseName, namespace, 1)
	if err != nil || len(revisions) == 0 {
		return "", nil
	}
	latest := revisions[len(revisions)-1]
	if !pendingReleaseStatuses[strings.ToLower(latest.Status)] {
		return "", nil
	}

	secretName := helmReleaseSecretName(releaseName, latest.Revision)
	err = cs.CoreV1().Secrets(namespace).Delete(ctx, secretName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("deleting stuck helm revision secret %s/%s: %w", namespace, secretName, err)
	}

	slog.Warn("recovered helm release stuck in pending state",
		"release", releaseName,
		"namespace", namespace,
		"revision", latest.Revision,
		"status", latest.Status,
	)
	return fmt.Sprintf("Recovered release %q: revision %d was stuck in %s; removed that revision so the deploy can continue.",
		releaseName, latest.Revision, latest.Status), nil
}
