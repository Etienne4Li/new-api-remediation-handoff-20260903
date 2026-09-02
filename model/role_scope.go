package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// applyAdminUserRoleScope adds the administrator visibility boundary to a
// query that contains an owner/user_id column.  Root is the only role that
// may enumerate every account.  Keeping this helper in model makes it harder
// for a new list/stat endpoint to accidentally rely on the router's
// AdminAuth check alone and leak peer/root data.
//
// The log database can be a separate ClickHouse/MySQL instance without a
// users table, so the predicate is expressed as a concrete id list obtained
// from the authoritative main database rather than a cross-database JOIN.
// Deleted users are included in the lookup: their historical usage/log rows
// remain visible to an administrator who was allowed to manage that user.
func applyAdminUserRoleScope(tx *gorm.DB, ownerColumn string, actorRole int) (*gorm.DB, error) {
	if tx == nil {
		return nil, errors.New("query is nil")
	}
	if actorRole == common.RoleRootUser {
		return tx, nil
	}
	if !common.IsValidateRole(actorRole) || actorRole < common.RoleAdminUser {
		// Fail closed for malformed context values.  A matched route should
		// already have rejected these, but model-level defence matters for
		// direct/controller calls and background reuse.
		return tx.Where("1 = 0"), nil
	}

	var userIDs []int
	if err := DB.Unscoped().Model(&User{}).Where("role < ?", actorRole).Pluck("id", &userIDs).Error; err != nil {
		return nil, err
	}
	if len(userIDs) == 0 {
		return tx.Where("1 = 0"), nil
	}
	return tx.Where(ownerColumn+" IN ?", userIDs), nil
}
