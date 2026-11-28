package database

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedImageDigestLogs stores the deploy and rollback logs of the
// LatestImageDigests checks (#811 R1, G2) and returns the expected merge for
// instance si-1.
func seedImageDigestLogs(t *testing.T, repo *GORMDeploymentLogRepository) map[string]string {
	t.Helper()
	ctx := context.Background()
	d := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	base := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	logs := []models.DeploymentLog{
		// Newest success: wins for app:v1.
		{ID: "l1", StackInstanceID: "si-1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: base.Add(10 * time.Hour), ImageDigests: `{"r/app:v1":"` + d("a") + `"}`},
		// Newer, but failed: skipped.
		{ID: "l2", StackInstanceID: "si-1", Action: models.DeployActionDeploy, Status: models.DeployLogError, StartedAt: base.Add(11 * time.Hour), ImageDigests: `{"r/app:v1":"` + d("e") + `"}`},
		// Newer, running: skipped.
		{ID: "l3", StackInstanceID: "si-1", Action: models.DeployActionDeploy, Status: models.DeployLogRunning, StartedAt: base.Add(12 * time.Hour), ImageDigests: `{"r/app:v1":"` + d("f") + `"}`},
		// Older success: app:v1 loses, worker:v2 comes from here.
		{ID: "l4", StackInstanceID: "si-1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: base.Add(9 * time.Hour), ImageDigests: `{"r/app:v1":"` + d("b") + `","r/worker:v2":"` + d("c") + `"}`},
		// Success without digests and one that does not parse: skipped.
		{ID: "l5", StackInstanceID: "si-1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: base.Add(8 * time.Hour)},
		{ID: "l6", StackInstanceID: "si-1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: base.Add(7 * time.Hour), ImageDigests: `not json`},
		// The newest successful rollback: it wins for app:v1 (G2).
		{ID: "l7", StackInstanceID: "si-1", Action: models.DeployActionRollback, Status: models.DeployLogSuccess, StartedAt: base.Add(13 * time.Hour), ImageDigests: `{"r/app:v1":"` + d("9") + `"}`},
		// A newer stop log and another instance: skipped.
		{ID: "l9", StackInstanceID: "si-1", Action: models.DeployActionStop, Status: models.DeployLogSuccess, StartedAt: base.Add(15 * time.Hour), ImageDigests: `{"r/app:v1":"` + d("6") + `"}`},
		{ID: "l8", StackInstanceID: "si-2", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: base.Add(14 * time.Hour), ImageDigests: `{"r/app:v1":"` + d("8") + `"}`},
	}
	// Past the newest MaxImageDigestLogs successful logs with digests:
	// not read.
	for i := 0; i < models.MaxImageDigestLogs; i++ {
		logs = append(logs, models.DeploymentLog{
			ID: "old-" + string(rune('a'+i)), StackInstanceID: "si-1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess,
			StartedAt: base.Add(time.Duration(i) * time.Minute), ImageDigests: `{"r/old:v` + string(rune('a'+i)) + `":"` + d("7") + `"}`,
		})
	}
	for i := range logs {
		require.NoError(t, repo.Create(ctx, &logs[i]))
	}
	// The 5 newest with digests: l7, l1, l4, l6 (does not parse) and old-e.
	return map[string]string{"r/app:v1": d("9"), "r/worker:v2": d("c"), "r/old:ve": d("7")}
}

func TestGORMDeploymentLogRepository_LatestImageDigests(t *testing.T) {
	t.Parallel()

	repo := setupDeploymentLogRepo(t)
	want := seedImageDigestLogs(t, repo)

	got, err := repo.LatestImageDigests(context.Background(), "si-1")
	require.NoError(t, err)
	assert.Equal(t, want, got)

	none, err := repo.LatestImageDigests(context.Background(), "missing")
	require.NoError(t, err)
	assert.Empty(t, none)
	assert.NotNil(t, none)

	checkImageDigestsOf(t, repo)

	// The column round-trips.
	l, err := repo.FindByID(context.Background(), "l1")
	require.NoError(t, err)
	assert.Contains(t, l.ImageDigests, "r/app:v1")
}

// checkImageDigestScanBound checks G6: LatestImageDigests reads only the
// newest MaxImageDigestScan successful deploy and rollback logs. A log with
// digests behind more newer logs without digests is not read (the
// documented trade-off); one just inside the window is.
func checkImageDigestScanBound(t *testing.T, repo *GORMDeploymentLogRepository) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	seed := func(instanceID string, newer int) {
		require.NoError(t, repo.Create(ctx, &models.DeploymentLog{
			ID: instanceID + "-old", StackInstanceID: instanceID, Action: models.DeployActionDeploy, Status: models.DeployLogSuccess,
			StartedAt: base, ImageDigests: `{"r/app:v1":"sha256:` + strings.Repeat("d", 64) + `"}`,
		}))
		for i := 0; i < newer; i++ {
			require.NoError(t, repo.Create(ctx, &models.DeploymentLog{
				ID: fmt.Sprintf("%s-n%02d", instanceID, i), StackInstanceID: instanceID, Action: models.DeployActionDeploy,
				Status: models.DeployLogSuccess, StartedAt: base.Add(time.Duration(i+1) * time.Minute),
			}))
		}
	}
	seed("scan-out", models.MaxImageDigestScan)
	seed("scan-in", models.MaxImageDigestScan-1)

	out, err := repo.LatestImageDigests(ctx, "scan-out")
	require.NoError(t, err)
	assert.Empty(t, out, "a log behind MaxImageDigestScan newer logs is not read")
	in, err := repo.LatestImageDigests(ctx, "scan-in")
	require.NoError(t, err)
	assert.Len(t, in, 1)
}

// checkImageDigestsOf checks J2: ListLatestByActions keeps to the small
// columns, and ImageDigestsOf reads the column of one log (the one-revision
// rollback copies it).
func checkImageDigestsOf(t *testing.T, repo *GORMDeploymentLogRepository) {
	t.Helper()
	ctx := context.Background()
	listed, err := repo.ListLatestByActions(ctx, "si-1", []string{models.DeployActionDeploy}, 1)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Empty(t, listed[0].ImageDigests, "ListLatestByActions loads only the small columns")

	got, err := repo.ImageDigestsOf(ctx, "l1")
	require.NoError(t, err)
	assert.Contains(t, got, "r/app:v1")
	got, err = repo.ImageDigestsOf(ctx, "l5")
	require.NoError(t, err)
	assert.Empty(t, got, "a log without digests")
	_, err = repo.ImageDigestsOf(ctx, "missing")
	assert.ErrorIs(t, err, dberrors.ErrNotFound)
}

func TestGORMDeploymentLogRepository_LatestImageDigestsScanBound(t *testing.T) {
	t.Parallel()
	checkImageDigestScanBound(t, setupDeploymentLogRepo(t))
}

func TestImageDigestsCodec(t *testing.T) {
	t.Parallel()

	assert.Empty(t, models.EncodeImageDigests(nil))
	enc := models.EncodeImageDigests(map[string]string{"r/app:v1": "sha256:x"})
	assert.JSONEq(t, `{"r/app:v1":"sha256:x"}`, enc)
	assert.Equal(t, map[string]string{"r/app:v1": "sha256:x", "r/b:v": "sha256:y"},
		models.MergeImageDigests([]string{enc, "", "bad", `{"r/app:v1":"sha256:old","r/b:v":"sha256:y","r/c:v":""}`}))
}

// TestDeployLogImageDigestsMigration_Idempotent checks migration 69: Up and
// Down can run twice.
func TestDeployLogImageDigestsMigration_Idempotent(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())
	mig := deployLogImageDigestsMigration()
	m := db.DB.Migrator()

	assert.True(t, m.HasColumn(&models.DeploymentLog{}, "ImageDigests"), "the migration chain adds the column")
	require.NoError(t, mig.Down(db.DB))
	require.NoError(t, mig.Down(db.DB))
	assert.False(t, m.HasColumn(&models.DeploymentLog{}, "ImageDigests"))
	require.NoError(t, mig.Up(db.DB))
	require.NoError(t, mig.Up(db.DB))
	assert.True(t, m.HasColumn(&models.DeploymentLog{}, "ImageDigests"))
	assert.True(t, m.HasColumn(&models.DeploymentLog{}, "ChartVersions"), "other columns stay")
}
