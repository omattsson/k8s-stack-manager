package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// Stack definition names are unique per owner. The check is done in the
// application (no unique index), because older databases can hold duplicate
// names; those duplicates stay until a user renames or deletes them.

// maxDefinitionNameAttempts limits the generated definition names tried by
// uniqueDefinitionName.
const maxDefinitionNameAttempts = 100

// errNoFreeDefinitionName is returned when no generated name is free.
var errNoFreeDefinitionName = errors.New("no free definition name")

// definitionNameTaken reports whether ownerID already owns a definition named
// name, other than the definition excludeID.
func definitionNameTaken(repo models.StackDefinitionRepository, ownerID, name, excludeID string) (bool, error) {
	defs, err := repo.FindByName(name)
	if err != nil {
		return false, err
	}
	for _, d := range defs {
		if d.OwnerID == ownerID && d.ID != excludeID {
			return true, nil
		}
	}
	return false, nil
}

// uniqueDefinitionName returns the first candidate(n), n = 1, 2, ..., that
// ownerID does not use as a definition name yet.
func uniqueDefinitionName(repo models.StackDefinitionRepository, ownerID string, candidate func(n int) string) (string, error) {
	for n := 1; n <= maxDefinitionNameAttempts; n++ {
		name := candidate(n)
		taken, err := definitionNameTaken(repo, ownerID, name, "")
		if err != nil {
			return "", err
		}
		if !taken {
			return name, nil
		}
	}
	return "", errNoFreeDefinitionName
}

// importedDefinitionName returns the n-th import name for base: base,
// "base (imported)", "base (imported 2)", ...
func importedDefinitionName(base string) func(n int) string {
	return func(n int) string {
		switch n {
		case 1:
			return base
		case 2:
			return base + " (imported)"
		default:
			return fmt.Sprintf("%s (imported %d)", base, n-1)
		}
	}
}

// numberedDefinitionName returns the n-th name for base: base, "base (2)", ...
func numberedDefinitionName(base string) func(n int) string {
	return func(n int) string {
		if n == 1 {
			return base
		}
		return fmt.Sprintf("%s (%d)", base, n)
	}
}

// respondDefinitionNameTaken checks the name rule and writes the response
// when the name is taken (409) or the check fails (500). It returns true when
// the caller must stop.
func respondDefinitionNameTaken(c *gin.Context, repo models.StackDefinitionRepository, ownerID, name, excludeID string) bool {
	taken, err := definitionNameTaken(repo, ownerID, name, excludeID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return true
	}
	if taken {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("A stack definition named %q already exists for this owner", name)})
		return true
	}
	return false
}
