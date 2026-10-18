package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"backend/internal/models"
	"backend/pkg/dberrors"
)

// DeleteInstanceWithOwnedDefinition deletes a stack instance in two steps.
//
//  1. One transaction deletes the instance record with its branch overrides,
//     value overrides, quota override and followers (DeleteInstanceRecord). An error
//     here is returned and nothing is deleted.
//  2. After that commit, a second transaction deletes the quick deploy
//     definition of the instance when nothing uses it any more
//     (CleanupOwnedDefinition). This step is best effort: an error is logged
//     and rolls back only the definition cleanup, never the instance delete.
func DeleteInstanceWithOwnedDefinition(runner TxRunner, inst *models.StackInstance) error {
	if err := runner.RunInTx(func(repos TxRepos) error {
		return DeleteInstanceRecord(repos, inst)
	}); err != nil {
		return err
	}
	if err := runner.RunInTx(func(repos TxRepos) error {
		return CleanupOwnedDefinition(repos, inst)
	}); err != nil {
		slog.Warn("owned definition cleanup failed; the instance is deleted",
			"instance_id", inst.ID, "definition_id", inst.StackDefinitionID, "error", err)
	}
	return nil
}

// DeleteInstanceRecord deletes the database record of a stack instance with
// its branch overrides, value overrides, quota override and followers. Call it inside
// TxRunner.RunInTx so that all deletes are atomic. Repositories that are not
// set in repos are skipped.
func DeleteInstanceRecord(repos TxRepos, inst *models.StackInstance) error {
	if repos.BranchOverride != nil {
		if err := repos.BranchOverride.DeleteByInstance(inst.ID); err != nil {
			return err
		}
	}
	if repos.ValueOverride != nil {
		if err := repos.ValueOverride.DeleteByInstance(inst.ID); err != nil {
			return err
		}
	}
	if repos.InstanceQuotaOverride != nil {
		// Delete returns not found when the instance has no override.
		if err := repos.InstanceQuotaOverride.Delete(context.Background(), inst.ID); err != nil && !errors.Is(err, dberrors.ErrNotFound) {
			return err
		}
	}
	if err := repos.StackInstance.Delete(inst.ID); err != nil {
		return err
	}
	// Followers after the instance row: a concurrent Follow holds a shared
	// lock on the row, so it either ends before this delete (and its row is
	// removed here) or finds no instance (see InstanceFollowerRepository.Follow).
	if repos.InstanceFollower != nil {
		if err := repos.InstanceFollower.DeleteByInstance(context.Background(), inst.ID); err != nil {
			return err
		}
	}
	return nil
}

// CleanupOwnedDefinition deletes the stack definition of inst, with its chart
// configs, when quick deploy created it (OwnerInstanceID is set) and no
// instance uses it any more. The definition is deleted when its owner is inst,
// or when its owner instance no longer exists (for example inst is a clone of
// a deleted quick deploy instance). Call it after the instance is deleted, in
// its own transaction. It returns nil when there is nothing to delete.
func CleanupOwnedDefinition(repos TxRepos, inst *models.StackInstance) error {
	if repos.StackDefinition == nil || repos.StackInstance == nil || inst.StackDefinitionID == "" {
		return nil
	}
	// Lock the definition row (SELECT ... FOR UPDATE). This only serializes
	// concurrent cleanups and updates of the definition: there is no foreign
	// key, so the lock does not block an instance insert that references it.
	def, err := repos.StackDefinition.FindByIDForUpdate(inst.StackDefinitionID)
	if err != nil {
		if errors.Is(err, dberrors.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("finding definition: %w", err)
	}
	if def.OwnerInstanceID == "" {
		return nil
	}
	if def.OwnerInstanceID != inst.ID {
		// Owned by another instance: delete only when that owner is gone.
		_, ownerErr := repos.StackInstance.FindByID(def.OwnerInstanceID)
		if ownerErr == nil {
			return nil
		}
		if !errors.Is(ownerErr, dberrors.ErrNotFound) {
			return fmt.Errorf("finding owner instance: %w", ownerErr)
		}
	}
	counts, err := repos.StackInstance.CountByDefinitionIDs([]string{def.ID})
	if err != nil {
		return fmt.Errorf("counting instances: %w", err)
	}
	if counts[def.ID] > 0 {
		slog.Info("owned definition kept: other instances use it",
			"instance_id", inst.ID, "definition_id", def.ID, "instances", counts[def.ID])
		return nil
	}
	if repos.ChartConfig != nil {
		if err := repos.ChartConfig.DeleteByDefinition(def.ID); err != nil {
			return fmt.Errorf("deleting charts: %w", err)
		}
	}
	if err := repos.StackDefinition.Delete(def.ID); err != nil {
		return fmt.Errorf("deleting definition: %w", err)
	}
	slog.Info("deleted quick deploy definition that no instance uses",
		"instance_id", inst.ID, "definition_id", def.ID, "owner_instance_id", def.OwnerInstanceID)
	return nil
}
