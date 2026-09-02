package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateSupportTicketQueuesUserNotificationButNotAdminNotification(t *testing.T) {
	db := setupSupportTicketTestDB(t)
	user := createSupportTicketTestUser(t, db, "outbox-user", common.RoleCommonUser)
	userTicket := createSupportTicketFixture(t, user, "User notification")

	var userOutbox SupportTicketNotificationOutbox
	require.NoError(t, db.Where("ticket_id = ?", userTicket.Id).First(&userOutbox).Error)
	assert.Equal(t, SupportTicketNotificationStatusPending, userOutbox.Status)
	assert.Equal(t, userTicket.CreatedTime, userOutbox.NextAttemptAt)

	admin := createSupportTicketTestUser(t, db, "outbox-admin", common.RoleAdminUser)
	adminTicket := createSupportTicketFixture(t, admin, "Admin notification")
	var adminOutboxCount int64
	require.NoError(t, db.Model(&SupportTicketNotificationOutbox{}).Where("ticket_id = ?", adminTicket.Id).Count(&adminOutboxCount).Error)
	assert.Zero(t, adminOutboxCount)
}

func TestSupportTicketNotificationLeaseRetryAndFailureLimit(t *testing.T) {
	db := setupSupportTicketTestDB(t)
	user := createSupportTicketTestUser(t, db, "outbox-lifecycle", common.RoleCommonUser)
	ticket := createSupportTicketFixture(t, user, "Lifecycle")

	var queued SupportTicketNotificationOutbox
	require.NoError(t, db.Where("ticket_id = ?", ticket.Id).First(&queued).Error)
	now := queued.NextAttemptAt

	claimed, err := ClaimSupportTicketNotifications("worker-a", now, now+60, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, 1, claimed[0].AttemptCount)

	competing, err := ClaimSupportTicketNotifications("worker-b", now+1, now+61, 1)
	require.NoError(t, err)
	assert.Empty(t, competing)
	assert.ErrorIs(t, RetrySupportTicketNotification(queued.Id, "worker-b", now+1, now+20), ErrSupportTicketNotificationLeaseLost)

	require.NoError(t, RetrySupportTicketNotification(queued.Id, "worker-a", now+1, now+20, "smtp unavailable"))
	var pending SupportTicketNotificationOutbox
	require.NoError(t, db.First(&pending, queued.Id).Error)
	assert.Equal(t, SupportTicketNotificationStatusPending, pending.Status)
	assert.Equal(t, "smtp unavailable", pending.LastError)

	currentTime := now + 20
	for attempt := pending.AttemptCount; attempt < SupportTicketNotificationMaxAttempts; attempt++ {
		claimed, err = ClaimSupportTicketNotifications("worker-a", currentTime, currentTime+60, 1)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		require.NoError(t, RetrySupportTicketNotification(queued.Id, "worker-a", currentTime, currentTime, "still unavailable"))
		currentTime++
	}

	var failed SupportTicketNotificationOutbox
	require.NoError(t, db.First(&failed, queued.Id).Error)
	assert.Equal(t, SupportTicketNotificationStatusFailed, failed.Status)
	assert.Equal(t, SupportTicketNotificationMaxAttempts, failed.AttemptCount)
	assert.Zero(t, failed.NextAttemptAt)
	assert.Equal(t, "still unavailable", failed.LastError)

	claimed, err = ClaimSupportTicketNotifications("worker-c", currentTime+100, currentTime+160, 1)
	require.NoError(t, err)
	assert.Empty(t, claimed)
}
