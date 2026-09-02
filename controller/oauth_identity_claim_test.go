package controller

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type identityClaimTestProvider struct{}

func (*identityClaimTestProvider) GetName() string { return "Identity Claim Test" }
func (*identityClaimTestProvider) IsEnabled() bool { return true }
func (*identityClaimTestProvider) ExchangeToken(context.Context, string, *gin.Context) (*oauth.OAuthToken, error) {
	return nil, nil
}
func (*identityClaimTestProvider) GetUserInfo(context.Context, *oauth.OAuthToken) (*oauth.OAuthUser, error) {
	return nil, nil
}
func (*identityClaimTestProvider) IsUserIDTaken(string) bool { return false }
func (*identityClaimTestProvider) FillUserByProviderID(user *model.User, providerUserID string) error {
	return model.DB.Where("github_id = ?", providerUserID).First(user).Error
}
func (*identityClaimTestProvider) SetProviderUserID(user *model.User, providerUserID string) {
	user.GitHubId = providerUserID
}
func (*identityClaimTestProvider) GetProviderPrefix() string    { return "identity_claim_" }
func (*identityClaimTestProvider) ProviderUserIDColumn() string { return "github_id" }

func setupIdentityClaimControllerTest(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousSecurity := common.GetSecurityRuntimeConfig()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.ExternalIdentityClaim{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) {
		cfg.RegisterEnabled = true
	})
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) {
			*cfg = previousSecurity
		})
	})
}

func TestFindOrCreateOAuthUserRecoversAfterClaimWinsRace(t *testing.T) {
	setupIdentityClaimControllerTest(t)

	owner := model.User{
		Username: "claim-owner",
		Password: "password",
		GitHubId: "github-race-id",
	}
	require.NoError(t, model.DB.Create(&owner).Error)
	require.NoError(t, model.DB.Transaction(func(tx *gorm.DB) error {
		return model.ClaimExternalIdentityWithTx(tx, model.ExternalIdentityProviderGitHub, owner.GitHubId, owner.Id)
	}))

	provider := &identityClaimTestProvider{}
	got, err := findOrCreateOAuthUser(nil, provider, &oauth.OAuthUser{ProviderUserID: owner.GitHubId}, "")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, owner.Id, got.Id)

	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&count).Error)
	assert.EqualValues(t, 1, count, "losing callback must roll back its provisional user")
}
