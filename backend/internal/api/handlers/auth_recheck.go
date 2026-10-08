package handlers

import (
	"errors"

	"backend/internal/models"
)

// errUserGone means the user was deleted while a token was being issued.
var errUserGone = errors.New("user_not_found")

// recheckUserActive re-reads the user from the repository (not from the login
// cache) after a token is signed and before it is returned or a refresh token
// is persisted. It closes the race where an admin disables the user between
// the first Disabled check and the signing: the user block applies only to
// tokens issued at or before the block, so a token signed just after the block
// would otherwise stay valid.
//
// Returns nil when the user is active, errAccountDisabled when the user is now
// disabled, errUserGone when the user no longer exists, or the repository
// error. Callers fail closed on every non-nil result.
func recheckUserActive(repo models.UserRepository, userID string) error {
	u, err := repo.FindByID(userID)
	if err != nil {
		if isNotFoundError(err) {
			return errUserGone
		}
		return err
	}
	if u.Disabled {
		return errAccountDisabled
	}
	return nil
}
