package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
)

func TestCanManageTargetRoleFailsClosedForUnknownRoles(t *testing.T) {
	tests := []struct {
		name       string
		actorRole  int
		targetRole int
		allowed    bool
	}{
		{name: "admin manages common user", actorRole: common.RoleAdminUser, targetRole: common.RoleCommonUser, allowed: true},
		{name: "admin manages guest user", actorRole: common.RoleAdminUser, targetRole: common.RoleGuestUser, allowed: true},
		{name: "admin cannot manage peer admin", actorRole: common.RoleAdminUser, targetRole: common.RoleAdminUser},
		{name: "admin cannot manage root", actorRole: common.RoleAdminUser, targetRole: common.RoleRootUser},
		{name: "root can manage root", actorRole: common.RoleRootUser, targetRole: common.RoleRootUser, allowed: true},
		{name: "unknown actor is denied", actorRole: 42, targetRole: common.RoleCommonUser},
		{name: "unknown target is denied for root", actorRole: common.RoleRootUser, targetRole: 42},
		{name: "negative actor is denied", actorRole: -1, targetRole: common.RoleGuestUser},
		{name: "negative target is denied", actorRole: common.RoleRootUser, targetRole: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.allowed, canManageTargetRole(tt.actorRole, tt.targetRole))
		})
	}
}
