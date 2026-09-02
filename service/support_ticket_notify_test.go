package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupSupportTicketNotificationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousMainDatabaseType := common.MainDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.LogDatabaseType())

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.SupportTicket{}, &model.SupportTicketMessage{}, &model.SupportTicketNotificationOutbox{}))

	t.Cleanup(func() {
		model.DB = previousDB
		common.SetDatabaseTypes(previousMainDatabaseType, common.LogDatabaseType())
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestPrepareSupportTicketNotificationUsesRootNotificationEmailAndEscapesHTML(t *testing.T) {
	db := setupSupportTicketNotificationTestDB(t)
	root := model.User{
		Username: "root",
		Password: "password",
		AffCode:  "root-support-ticket",
		Role:     common.RoleRootUser,
		Status:   common.UserStatusEnabled,
		Email:    "root@example.com",
		Group:    "default",
	}
	root.SetSetting(dto.UserSetting{NotificationEmail: "alerts@example.com"})
	require.NoError(t, db.Create(&root).Error)

	ticket := model.SupportTicket{
		Id:       42,
		Title:    "<script>alert(1)</script>",
		Category: model.SupportTicketCategoryAPI,
		Priority: model.SupportTicketPriorityHigh,
	}
	subject, recipient, body, err := prepareSupportTicketNotification(ticket, "alice <admin>", "line one\n<script>alert(2)</script>")

	require.NoError(t, err)
	assert.Equal(t, "alerts@example.com", recipient)
	assert.Contains(t, subject, "#42")
	assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
	assert.Contains(t, body, "alice &lt;admin&gt;")
	assert.Contains(t, body, "line one<br>&lt;script&gt;alert(2)&lt;/script&gt;")
	assert.NotContains(t, body, "<script>")
}

func TestPrepareSupportTicketNotificationSkipsRootWithoutEmail(t *testing.T) {
	db := setupSupportTicketNotificationTestDB(t)
	root := model.User{
		Username: "root",
		Password: "password",
		AffCode:  "root-support-ticket-no-email",
		Role:     common.RoleRootUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	require.NoError(t, db.Create(&root).Error)

	_, recipient, body, err := prepareSupportTicketNotification(model.SupportTicket{Id: 1, Title: "A ticket"}, "alice", "content")

	require.NoError(t, err)
	assert.Empty(t, recipient)
	assert.Empty(t, body)
}

func TestPrepareSupportTicketNotificationReturnsDatabaseErrors(t *testing.T) {
	previousDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previousDB })

	_, _, _, err := prepareSupportTicketNotification(model.SupportTicket{Id: 1, Title: "A ticket"}, "alice", "content")

	assert.Error(t, err)
}

func TestSupportTicketNotificationWorkerSendsAndMarksSent(t *testing.T) {
	db := setupSupportTicketNotificationTestDB(t)
	root := model.User{
		Username: "worker-root",
		Password: "password",
		AffCode:  "worker-root-aff",
		Role:     common.RoleRootUser,
		Status:   common.UserStatusEnabled,
		Email:    "root@example.com",
		Group:    "default",
	}
	require.NoError(t, db.Create(&root).Error)
	user := model.User{
		Username: "worker-user",
		Password: "password",
		AffCode:  "worker-user-aff",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	require.NoError(t, db.Create(&user).Error)
	ticket := model.SupportTicket{
		UserId:   user.Id,
		Title:    "SMTP worker",
		Category: model.SupportTicketCategoryAPI,
		Priority: model.SupportTicketPriorityNormal,
	}
	require.NoError(t, model.CreateSupportTicket(&ticket, user.Role, "Please notify support"))

	currentTime := time.Unix(ticket.CreatedTime+1, 0)
	sendCount := 0
	worker := supportTicketNotificationWorker{
		id:  "worker-test",
		now: func() time.Time { return currentTime },
		sendEmail: func(subject string, recipient string, body string) error {
			sendCount++
			assert.Contains(t, subject, "#"+fmt.Sprint(ticket.Id))
			assert.Equal(t, "root@example.com", recipient)
			assert.Contains(t, body, "Please notify support")
			return nil
		},
	}

	processed, err := worker.processNext()
	require.NoError(t, err)
	assert.True(t, processed)
	assert.Equal(t, 1, sendCount)

	var outbox model.SupportTicketNotificationOutbox
	require.NoError(t, db.Where("ticket_id = ?", ticket.Id).First(&outbox).Error)
	assert.Equal(t, model.SupportTicketNotificationStatusSent, outbox.Status)
	assert.Equal(t, 1, outbox.AttemptCount)

	processed, err = worker.processNext()
	require.NoError(t, err)
	assert.False(t, processed)
	assert.Equal(t, 1, sendCount)
}

func TestSupportTicketNotificationWorkerRetriesAndPersistsFailure(t *testing.T) {
	db := setupSupportTicketNotificationTestDB(t)
	root := model.User{
		Username: "retry-root",
		Password: "password",
		AffCode:  "retry-root-aff",
		Role:     common.RoleRootUser,
		Status:   common.UserStatusEnabled,
		Email:    "root@example.com",
		Group:    "default",
	}
	require.NoError(t, db.Create(&root).Error)
	user := model.User{
		Username: "retry-user",
		Password: "password",
		AffCode:  "retry-user-aff",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	require.NoError(t, db.Create(&user).Error)
	ticket := model.SupportTicket{
		UserId:   user.Id,
		Title:    "SMTP retry",
		Category: model.SupportTicketCategoryAPI,
		Priority: model.SupportTicketPriorityNormal,
	}
	require.NoError(t, model.CreateSupportTicket(&ticket, user.Role, "Retry this message"))

	currentTime := time.Unix(ticket.CreatedTime+1, 0)
	worker := supportTicketNotificationWorker{
		id:        "retry-worker-test",
		now:       func() time.Time { return currentTime },
		sendEmail: func(string, string, string) error { return errors.New("SMTP unavailable") },
	}

	processed, err := worker.processNext()
	require.NoError(t, err)
	assert.True(t, processed)

	var outbox model.SupportTicketNotificationOutbox
	require.NoError(t, db.Where("ticket_id = ?", ticket.Id).First(&outbox).Error)
	assert.Equal(t, model.SupportTicketNotificationStatusPending, outbox.Status)
	assert.Equal(t, "SMTP unavailable", outbox.LastError)
	assert.Equal(t, currentTime.Add(supportTicketNotificationRetryBase).Unix(), outbox.NextAttemptAt)
}
