package database

import (
	"encoding/json"
	"testing"
	"time"

	"backend/internal/database/schema"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigration_ReleaseTemplateWorkingCopies checks migration 44: published or
// referenced templates whose latest snapshot is missing, legacy or stale get a
// snapshot of the working copy; other templates get none; a re-run adds
// nothing.
func TestMigration_ReleaseTemplateWorkingCopies(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())
	require.NoError(t, db.DB.Unscoped().Where("version = ?", "20261009000044").Delete(&schema.SchemaVersion{}).Error)

	now := time.Now().UTC().Add(-time.Hour)
	tmpl := func(id, version string, published bool) models.StackTemplate {
		return models.StackTemplate{ID: id, Name: id, Version: version, OwnerID: "u-" + id, IsPublished: published, CreatedAt: now, UpdatedAt: now}
	}
	templates := []models.StackTemplate{
		tmpl("pub-none", "1.2.0", true),      // published, no snapshot -> new
		tmpl("pub-drift", "1.1.0", true),     // latest snapshot differs -> new
		tmpl("pub-legacy", "1.0.0", true),    // legacy latest snapshot -> new
		tmpl("pub-same", "1.0.0", true),      // latest equals working copy -> none
		tmpl("unpub-ref", "2.0.0", false),    // unpublished, referenced, drift -> new
		tmpl("unpub-noref", "2.0.0", false),  // unpublished, not referenced -> none
		tmpl("unpub-nosnap", "0.1.0", false), // unpublished, referenced, never released -> none
		tmpl("pub-nover", "", true),          // published, no version, no snapshot -> new "1.0.0"
	}
	for i := range templates {
		require.NoError(t, db.DB.Create(&templates[i]).Error)
	}
	for _, id := range []string{"pub-none", "pub-drift", "pub-legacy", "pub-same", "unpub-ref", "unpub-noref", "unpub-nosnap", "pub-nover"} {
		require.NoError(t, db.DB.Create(&models.TemplateChartConfig{
			ID: "c-" + id, StackTemplateID: id, ChartName: "web", ChartVersion: "2.0.0", DefaultValues: "a: 2", DeployOrder: 1, CreatedAt: now,
		}).Error)
	}

	snapshotOf := func(id, version, values string, schemaVersion int) string {
		s := models.TemplateSnapshot{
			SchemaVersion: schemaVersion,
			Template:      models.TemplateSnapshotData{Name: id, Version: version},
			Charts:        []models.TemplateChartSnapshotData{{ChartName: "web", DefaultValues: values, SortOrder: 1}},
		}
		if schemaVersion >= 1 {
			s.Charts[0].ChartVersion = "2.0.0"
		}
		b, err := json.Marshal(s)
		require.NoError(t, err)
		return string(b)
	}
	old := []models.TemplateVersion{
		{ID: "v-drift", TemplateID: "pub-drift", Version: "1.0.0", Snapshot: snapshotOf("pub-drift", "1.0.0", "a: 1", 1)},
		{ID: "v-legacy", TemplateID: "pub-legacy", Version: "1.0.0", Snapshot: snapshotOf("pub-legacy", "1.0.0", "a: 2", 0)},
		{ID: "v-same", TemplateID: "pub-same", Version: "1.0.0", Snapshot: snapshotOf("pub-same", "1.0.0", "a: 2", 1)},
		{ID: "v-ref", TemplateID: "unpub-ref", Version: "1.0.0", Snapshot: snapshotOf("unpub-ref", "1.0.0", "a: 1", 1)},
		{ID: "v-noref", TemplateID: "unpub-noref", Version: "1.0.0", Snapshot: snapshotOf("unpub-noref", "1.0.0", "a: 1", 1)},
	}
	for i := range old {
		old[i].CreatedAt = now
		require.NoError(t, db.DB.Create(&old[i]).Error)
	}
	for _, src := range []string{"unpub-ref", "unpub-nosnap"} {
		require.NoError(t, db.DB.Create(&models.StackDefinition{
			ID: "d-" + src, Name: "d-" + src, OwnerID: "u1", SourceTemplateID: src, SourceTemplateVersion: "1.0.0", CreatedAt: now, UpdatedAt: now,
		}).Error)
	}

	require.NoError(t, db.AutoMigrate())

	count := func(templateID string) int64 {
		var n int64
		require.NoError(t, db.DB.Model(&models.TemplateVersion{}).Where("template_id = ?", templateID).Count(&n).Error)
		return n
	}
	tests := []struct {
		templateID string
		want       int64
	}{
		{"pub-none", 1},
		{"pub-drift", 2},
		{"pub-legacy", 2},
		{"pub-same", 1},
		{"unpub-ref", 2},
		{"unpub-noref", 1},
		{"unpub-nosnap", 0},
		{"pub-nover", 1},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, count(tt.templateID), tt.templateID)
	}

	repo := NewGORMTemplateVersionRepository(db.DB)
	for _, id := range []string{"pub-none", "pub-drift", "pub-legacy", "unpub-ref"} {
		latest, err := repo.GetLatestByTemplate(t.Context(), id)
		require.NoError(t, err, id)
		assert.Equal(t, backfillSnapshotChangeSummary, latest.ChangeSummary, id)
		assert.Equal(t, "u-"+id, latest.CreatedBy, id)
		var snap models.TemplateSnapshot
		require.NoError(t, json.Unmarshal([]byte(latest.Snapshot), &snap))
		assert.Equal(t, models.TemplateSnapshotSchemaVersion, snap.SchemaVersion, id)
		require.Len(t, snap.Charts, 1, id)
		assert.Equal(t, "a: 2", snap.Charts[0].DefaultValues, id)
		assert.Equal(t, "2.0.0", snap.Charts[0].ChartVersion, id)
	}
	latest, err := repo.GetLatestByTemplate(t.Context(), "pub-drift")
	require.NoError(t, err)
	assert.Equal(t, "1.1.0", latest.Version)

	// An empty version becomes "1.0.0" in the snapshot and the working copy.
	latest, err = repo.GetLatestByTemplate(t.Context(), "pub-nover")
	require.NoError(t, err)
	assert.Equal(t, backfillDefaultVersion, latest.Version)
	var nover models.StackTemplate
	require.NoError(t, db.DB.First(&nover, "id = ?", "pub-nover").Error)
	assert.Equal(t, backfillDefaultVersion, nover.Version)

	// Idempotent: a re-run adds nothing.
	require.NoError(t, db.DB.Unscoped().Where("version = ?", "20261009000044").Delete(&schema.SchemaVersion{}).Error)
	require.NoError(t, db.AutoMigrate())
	for _, tt := range tests {
		assert.Equal(t, tt.want, count(tt.templateID), "re-run: "+tt.templateID)
	}

	require.NoError(t, backfillPublishedTemplateSnapshotsMigration().Down(db.DB))
}
