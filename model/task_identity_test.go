package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// This is a database-level contract: two local tasks must not be able to
// claim the same provider identity within one channel and platform.  An
// application-side map is insufficient because submitters and pollers may run
// in different processes.
func TestTaskUpstreamIdentityDatabaseFence(t *testing.T) {
	truncateTables(t)

	assert.True(t, DB.Migrator().HasIndex(&Task{}, taskUpstreamIdentityIndexName))

	first := &Task{
		TaskID:    "local-task-identity-a",
		Platform:  constant.TaskPlatform("video"),
		ChannelId: 8123,
		PrivateData: TaskPrivateData{
			UpstreamTaskID: "provider-task-same",
		},
	}
	second := &Task{
		TaskID:    "local-task-identity-b",
		Platform:  first.Platform,
		ChannelId: first.ChannelId,
		PrivateData: TaskPrivateData{
			UpstreamTaskID: first.PrivateData.UpstreamTaskID,
		},
	}
	require.NoError(t, DB.Create(first).Error)
	require.Error(t, DB.Create(second).Error)

	var count int64
	require.NoError(t, DB.Model(&Task{}).
		Where("channel_id = ? AND platform = ?", first.ChannelId, first.Platform).
		Count(&count).Error)
	assert.EqualValues(t, 1, count)

	// The same provider id is valid on another channel or platform; the
	// composite fence must not over-reject those independent namespaces.
	otherChannel := &Task{
		TaskID:    "local-task-identity-c",
		Platform:  first.Platform,
		ChannelId: first.ChannelId + 1,
		PrivateData: TaskPrivateData{
			UpstreamTaskID: first.PrivateData.UpstreamTaskID,
		},
	}
	require.NoError(t, DB.Create(otherChannel).Error)
	otherPlatform := &Task{
		TaskID:    "local-task-identity-d",
		Platform:  constant.TaskPlatform("another-video"),
		ChannelId: first.ChannelId,
		PrivateData: TaskPrivateData{
			UpstreamTaskID: first.PrivateData.UpstreamTaskID,
		},
	}
	require.NoError(t, DB.Create(otherPlatform).Error)
}

func TestTaskGetUpstreamTaskIDTrimsProviderAndFallbackIDs(t *testing.T) {
	assert.Equal(t, "provider-task", (&Task{
		TaskID:      " public-task ",
		PrivateData: TaskPrivateData{UpstreamTaskID: "  provider-task  "},
	}).GetUpstreamTaskID())
	assert.Equal(t, "public-task", (&Task{TaskID: " public-task "}).GetUpstreamTaskID())
	assert.Empty(t, (&Task{TaskID: "   "}).GetUpstreamTaskID())
}

func TestTaskUpstreamIdentityCannotBeChangedByClearingDigest(t *testing.T) {
	truncateTables(t)
	task := &Task{
		TaskID:    "public-task-id",
		Platform:  constant.TaskPlatform("video"),
		ChannelId: 8200,
		PrivateData: TaskPrivateData{
			UpstreamTaskID: "provider-task-original",
		},
	}
	require.NoError(t, DB.Create(task).Error)
	var loaded Task
	require.NoError(t, DB.First(&loaded, task.ID).Error)

	// Simulate a caller that clears the denormalized field while replacing the
	// provider ID. BeforeSave must consult the persisted identity rather than
	// treating a nil candidate as a fresh insert.
	loaded.UpstreamTaskIdentity = nil
	loaded.PrivateData.UpstreamTaskID = "provider-task-attacker"
	err := DB.Save(&loaded).Error
	require.ErrorIs(t, err, ErrTaskUpstreamIdentityImmutable)

	var persisted Task
	require.NoError(t, DB.First(&persisted, task.ID).Error)
	require.NotNil(t, persisted.UpstreamTaskIdentity)
	want, ok := TaskUpstreamIdentityDigest("provider-task-original")
	require.True(t, ok)
	assert.Equal(t, want, *persisted.UpstreamTaskIdentity)
}

func TestTaskUpstreamIdentityProtectsHistoricalTaskIDFallback(t *testing.T) {
	truncateTables(t)
	task := &Task{
		TaskID:    "historical-provider-id",
		Platform:  constant.TaskPlatform("video"),
		ChannelId: 8201,
	}
	require.NoError(t, DB.Create(task).Error)
	var loaded Task
	require.NoError(t, DB.First(&loaded, task.ID).Error)
	loaded.UpstreamTaskIdentity = nil
	loaded.TaskID = "replacement-provider-id"
	err := DB.Save(&loaded).Error
	require.ErrorIs(t, err, ErrTaskUpstreamIdentityImmutable)
}

func TestTaskUpstreamIdentityRejectsLegacyFallbackRebinding(t *testing.T) {
	truncateTables(t)
	task := &Task{
		TaskID:    "historical-provider-id-rebind",
		Platform:  constant.TaskPlatform("video"),
		ChannelId: 8202,
	}
	require.NoError(t, DB.Create(task).Error)
	var loaded Task
	require.NoError(t, DB.First(&loaded, task.ID).Error)
	loaded.PrivateData.UpstreamTaskID = "different-provider-id"
	err := DB.Save(&loaded).Error
	require.ErrorIs(t, err, ErrTaskUpstreamIdentityImmutable)

	var persisted Task
	require.NoError(t, DB.First(&persisted, task.ID).Error)
	assert.Empty(t, persisted.PrivateData.UpstreamTaskID)
	assert.Equal(t, task.TaskID, persisted.TaskID)
}

func TestTaskUpstreamIdentityRejectsCallerSuppliedDigestOnInsert(t *testing.T) {
	truncateTables(t)
	wrong := "0000000000000000000000000000000000000000000000000000000000000000"
	task := &Task{
		TaskID:               "task-invalid-supplied-digest",
		Platform:             constant.TaskPlatform("video"),
		ChannelId:            8203,
		UpstreamTaskIdentity: &wrong,
	}
	err := DB.Create(task).Error
	require.ErrorIs(t, err, ErrTaskUpstreamIdentityImmutable)
}

func newTaskIdentityMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&Task{}))
	return db
}

func TestTaskIdentityMigrationBackfillsLegacyPrivateData(t *testing.T) {
	db := newTaskIdentityMigrationDB(t)
	// Simulate a pre-deployment row: the JSON provider ID exists but the new
	// denormalized column was not populated yet.
	legacy := &Task{
		TaskID:    "legacy-public-id",
		Platform:  constant.TaskPlatform("video"),
		ChannelId: 77,
		PrivateData: TaskPrivateData{
			UpstreamTaskID: "legacy-provider-id",
		},
	}
	require.NoError(t, db.Create(legacy).Error)
	require.NoError(t, db.Model(&Task{}).Where("id = ?", legacy.ID).Update("upstream_task_identity", nil).Error)
	require.NoError(t, db.Migrator().DropIndex(&Task{}, taskUpstreamIdentityIndexName))

	require.NoError(t, MigrateTaskIdentitySchemaExpand(db, common.DatabaseTypeSQLite))
	var got Task
	require.NoError(t, db.First(&got, legacy.ID).Error)
	want, ok := TaskUpstreamIdentityDigest("legacy-provider-id")
	require.True(t, ok)
	require.NotNil(t, got.UpstreamTaskIdentity)
	assert.Equal(t, want, *got.UpstreamTaskIdentity)
	assert.True(t, db.Migrator().HasIndex(&Task{}, taskUpstreamIdentityIndexName))
}

func TestTaskIdentityMigrationRejectsDuplicateLegacyProviderIDs(t *testing.T) {
	db := newTaskIdentityMigrationDB(t)
	first := &Task{TaskID: "legacy-a", Platform: constant.TaskPlatform("video"), ChannelId: 88,
		PrivateData: TaskPrivateData{UpstreamTaskID: "duplicate-provider-id"}}
	second := &Task{TaskID: "legacy-b", Platform: first.Platform, ChannelId: first.ChannelId,
		PrivateData: TaskPrivateData{UpstreamTaskID: "duplicate-provider-id"}}
	require.NoError(t, db.Create(first).Error)
	require.NoError(t, db.Migrator().DropIndex(&Task{}, taskUpstreamIdentityIndexName))
	require.NoError(t, db.Create(second).Error)
	require.NoError(t, db.Model(&Task{}).Where("id IN ?", []int64{first.ID, second.ID}).Update("upstream_task_identity", nil).Error)

	err := MigrateTaskIdentitySchemaExpand(db, common.DatabaseTypeSQLite)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "duplicate")
	assert.False(t, db.Migrator().HasIndex(&Task{}, taskUpstreamIdentityIndexName), "ambiguous rows must not receive a uniqueness fence")
}

func TestTaskIdentityMigrationUsesTaskIDWhenPrivateDataMissing(t *testing.T) {
	db := newTaskIdentityMigrationDB(t)
	legacy := &Task{TaskID: "historical-provider-id", Platform: constant.TaskPlatform("video"), ChannelId: 99}
	require.NoError(t, db.Create(legacy).Error)
	require.NoError(t, db.Model(&Task{}).Where("id = ?", legacy.ID).Update("upstream_task_identity", nil).Error)
	require.NoError(t, db.Migrator().DropIndex(&Task{}, taskUpstreamIdentityIndexName))

	require.NoError(t, MigrateTaskIdentitySchemaExpand(db, common.DatabaseTypeSQLite))
	var got Task
	require.NoError(t, db.First(&got, legacy.ID).Error)
	want, ok := TaskUpstreamIdentityDigest(legacy.TaskID)
	require.True(t, ok)
	require.NotNil(t, got.UpstreamTaskIdentity)
	assert.Equal(t, want, *got.UpstreamTaskIdentity)
}

func TestTaskPrivateDataAndPropertiesScanStringValues(t *testing.T) {
	var private TaskPrivateData
	require.NoError(t, private.Scan(`{"upstream_task_id":"provider-string"}`))
	assert.Equal(t, "provider-string", private.UpstreamTaskID)
	var properties Properties
	require.NoError(t, properties.Scan(`{"origin_model_name":"model-string"}`))
	assert.Equal(t, "model-string", properties.OriginModelName)
}

func TestTaskGetDataDecodesIntoProvidedTarget(t *testing.T) {
	task := &Task{}
	task.Data = json.RawMessage(`{"value":"decoded"}`)
	var target struct {
		Value string `json:"value"`
	}
	require.NoError(t, task.GetData(&target))
	assert.Equal(t, "decoded", target.Value)
	assert.Error(t, task.GetData(nil))
}
