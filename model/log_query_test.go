package model

import (
	"bytes"
	"log"
	"math"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func newLogQueryTestDB(t *testing.T, output *bytes.Buffer) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.New(
			log.New(output, "", 0),
			gormlogger.Config{LogLevel: gormlogger.Info},
		),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))
	return db
}

func TestBoundedLogCountCapsBeforeAggregation(t *testing.T) {
	var sqlOutput bytes.Buffer
	db := newLogQueryTestDB(t, &sqlOutput)

	rows := make([]Log, logSearchCountLimit+1)
	for i := range rows {
		rows[i] = Log{UserId: 42, CreatedAt: int64(i + 1), Type: LogTypeConsume}
	}
	require.NoError(t, db.CreateInBatches(&rows, 500).Error)
	sqlOutput.Reset()

	total, err := boundedLogCount(db.Model(&Log{}).Where("user_id = ?", 42), logSearchCountLimit)
	require.NoError(t, err)
	assert.EqualValues(t, logSearchCountLimit, total)

	// The old implementation used `Limit(...).Count`, which emits an
	// aggregate COUNT and still scans every matching row. The bounded helper
	// must issue a lightweight primary-key query with only limit+1 rows.
	queries := strings.ToLower(sqlOutput.String())
	assert.Contains(t, queries, "select `id` from `logs` where user_id = 42 limit 10001")
	assert.NotContains(t, queries, "count(*)")
}

func TestBoundedLogCountReturnsExactSmallResult(t *testing.T) {
	var sqlOutput bytes.Buffer
	db := newLogQueryTestDB(t, &sqlOutput)
	require.NoError(t, db.Create(&Log{UserId: 7, CreatedAt: 1}).Error)
	require.NoError(t, db.Create(&Log{UserId: 7, CreatedAt: 2}).Error)

	total, err := boundedLogCount(db.Model(&Log{}).Where("user_id = ?", 7), logSearchCountLimit)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
}

func TestGetAllLogsUsesBoundedTotal(t *testing.T) {
	var sqlOutput bytes.Buffer
	db := newLogQueryTestDB(t, &sqlOutput)
	previousLogDB := LOG_DB
	previousLogDatabaseType := common.LogDatabaseType()
	LOG_DB = db
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousLogDatabaseType)
	})

	rows := make([]Log, logSearchCountLimit+1)
	for i := range rows {
		rows[i] = Log{CreatedAt: int64(i + 1), Type: LogTypeConsume}
	}
	require.NoError(t, db.CreateInBatches(&rows, 500).Error)
	sqlOutput.Reset()

	logs, total, err := GetAllLogs(
		LogTypeUnknown, 0, 0, "", "", "", 0, 1, 0, "", "", "",
	)
	require.NoError(t, err)
	assert.Len(t, logs, 1)
	assert.EqualValues(t, logSearchCountLimit, total)

	queries := strings.ToLower(sqlOutput.String())
	assert.Contains(t, queries, "select `id` from `logs` limit 10001")
	assert.NotContains(t, queries, "count(*)")
}

func TestGetAllLogsBoundsUntrustedPagination(t *testing.T) {
	var sqlOutput bytes.Buffer
	db := newLogQueryTestDB(t, &sqlOutput)
	previousLogDB := LOG_DB
	previousLogDatabaseType := common.LogDatabaseType()
	LOG_DB = db
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousLogDatabaseType)
	})
	require.NoError(t, db.Create(&Log{CreatedAt: 1, Type: LogTypeConsume}).Error)
	sqlOutput.Reset()

	logs, _, err := GetAllLogs(
		LogTypeUnknown, 0, 0, "", "", "", math.MaxInt, -1, 0, "", "", "",
	)
	require.NoError(t, err)
	assert.Empty(t, logs)

	queries := strings.ToLower(sqlOutput.String())
	// A hostile page/size must not disable LIMIT or force a max-int OFFSET.
	assert.NotContains(t, queries, "offset 9223372036854775807")
	assert.Contains(t, queries, "limit 10 offset 10000")
}

func TestGetUserLogsBoundsUntrustedPagination(t *testing.T) {
	var sqlOutput bytes.Buffer
	db := newLogQueryTestDB(t, &sqlOutput)
	previousLogDB := LOG_DB
	previousLogDatabaseType := common.LogDatabaseType()
	LOG_DB = db
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousLogDatabaseType)
	})
	require.NoError(t, db.Create(&Log{UserId: 7, CreatedAt: 1, Type: LogTypeConsume}).Error)
	sqlOutput.Reset()

	logs, total, err := GetUserLogs(
		7, LogTypeUnknown, 0, 0, "", "", math.MaxInt, 0, "", "", "",
	)
	require.NoError(t, err)
	assert.Empty(t, logs)
	assert.EqualValues(t, 1, total)

	queries := strings.ToLower(sqlOutput.String())
	assert.NotContains(t, queries, "offset 9223372036854775807")
	assert.Contains(t, queries, "limit 10 offset 10000")
}
