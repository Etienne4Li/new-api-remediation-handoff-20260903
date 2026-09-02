package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/codex"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFetchCodexChannelWhamDataRejectsNonPositiveChannelID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, rawID := range []string{"", "0", "-1", "not-a-number"} {
		t.Run(rawID, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest("GET", "/api/channel/"+rawID+"/codex/usage", nil)
			ctx.Params = gin.Params{{Key: "id", Value: rawID}}

			fetchCodexChannelWhamData(ctx, nil, "test", "failed")

			require.Equal(t, 200, recorder.Code)
			require.Contains(t, recorder.Body.String(), "invalid channel id")
		})
	}
}

func TestPersistRefreshedCodexCredentialPropagatesDatabaseError(t *testing.T) {
	originalDB := model.DB
	brokenDB, err := gorm.Open(sqlite.Open("file:codex-credential-persist-closed?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := brokenDB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	model.DB = brokenDB
	t.Cleanup(func() { model.DB = originalDB })

	err = persistRefreshedCodexCredential(42, &codex.OAuthKey{
		AccessToken:  "access",
		RefreshToken: "refresh",
		AccountID:    "acct",
	})
	require.Error(t, err)
}
