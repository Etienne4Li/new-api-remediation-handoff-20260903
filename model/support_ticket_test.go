package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupSupportTicketTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousMainDatabaseType := common.MainDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.LogDatabaseType())

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&User{}, &SupportTicket{}, &SupportTicketMessage{}, &SupportTicketNotificationOutbox{}))

	t.Cleanup(func() {
		DB = previousDB
		common.SetDatabaseTypes(previousMainDatabaseType, common.LogDatabaseType())
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func createSupportTicketTestUser(t *testing.T, db *gorm.DB, username string, role int) User {
	t.Helper()
	user := User{
		Username: username,
		Password: "password",
		AffCode:  "aff-" + username,
		Role:     role,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	require.NoError(t, db.Create(&user).Error)
	return user
}

func createSupportTicketFixture(t *testing.T, user User, title string) SupportTicket {
	t.Helper()
	ticket := SupportTicket{
		UserId:   user.Id,
		Title:    title,
		Category: SupportTicketCategoryAPI,
		Priority: SupportTicketPriorityNormal,
	}
	require.NoError(t, CreateSupportTicket(&ticket, user.Role, "Initial request"))
	return ticket
}

func TestCreateSupportTicketPersistsInitialMessageAndScopesLists(t *testing.T) {
	db := setupSupportTicketTestDB(t)
	alice := createSupportTicketTestUser(t, db, "ticket-alice", common.RoleCommonUser)
	bob := createSupportTicketTestUser(t, db, "ticket-bob", common.RoleCommonUser)
	admin := createSupportTicketTestUser(t, db, "ticket-admin", common.RoleAdminUser)

	aliceTicket := createSupportTicketFixture(t, alice, "Alice API request")
	createSupportTicketFixture(t, bob, "Bob billing request")

	aliceItems, aliceTotal, err := ListSupportTickets(alice.Id, alice.Role, SupportTicketFilter{}, 0, 20)
	require.NoError(t, err)
	require.Len(t, aliceItems, 1)
	assert.EqualValues(t, 1, aliceTotal)
	assert.Equal(t, aliceTicket.Id, aliceItems[0].Id)
	assert.Equal(t, alice.Username, aliceItems[0].Username)
	assert.EqualValues(t, 1, aliceItems[0].MessageCount)

	adminItems, adminTotal, err := ListSupportTickets(admin.Id, admin.Role, SupportTicketFilter{}, 0, 20)
	require.NoError(t, err)
	require.Len(t, adminItems, 2)
	assert.EqualValues(t, 2, adminTotal)

	detail, err := GetSupportTicketDetail(aliceTicket.Id, alice.Id, alice.Role)
	require.NoError(t, err)
	require.Len(t, detail.Messages, 1)
	assert.Equal(t, "Initial request", detail.Messages[0].Content)
	assert.Equal(t, alice.Username, detail.Messages[0].AuthorName)
	assert.Equal(t, SupportTicketStatusOpen, detail.Ticket.Status)

	_, err = GetSupportTicketDetail(aliceTicket.Id, bob.Id, bob.Role)
	assert.ErrorIs(t, err, ErrSupportTicketNotFound)
}

func TestSupportTicketReplyAndStatusLifecycle(t *testing.T) {
	db := setupSupportTicketTestDB(t)
	user := createSupportTicketTestUser(t, db, "ticket-owner", common.RoleCommonUser)
	other := createSupportTicketTestUser(t, db, "ticket-other", common.RoleCommonUser)
	admin := createSupportTicketTestUser(t, db, "ticket-operator", common.RoleAdminUser)
	ticket := createSupportTicketFixture(t, user, "Lifecycle request")

	_, err := AddSupportTicketMessage(ticket.Id, other.Id, other.Role, "Unauthorized reply")
	assert.ErrorIs(t, err, ErrSupportTicketNotFound)

	_, err = AddSupportTicketMessage(ticket.Id, admin.Id, admin.Role, "We are investigating")
	require.NoError(t, err)
	detail, err := GetSupportTicketDetail(ticket.Id, user.Id, user.Role)
	require.NoError(t, err)
	assert.Equal(t, SupportTicketStatusInProgress, detail.Ticket.Status)
	assert.Equal(t, SupportTicketReplyByAdmin, detail.Ticket.LastReplyBy)

	_, err = AddSupportTicketMessage(ticket.Id, user.Id, user.Role, "Additional details")
	require.NoError(t, err)
	detail, err = GetSupportTicketDetail(ticket.Id, user.Id, user.Role)
	require.NoError(t, err)
	assert.Equal(t, SupportTicketStatusOpen, detail.Ticket.Status)
	assert.Equal(t, SupportTicketReplyByUser, detail.Ticket.LastReplyBy)

	_, err = UpdateSupportTicketStatus(ticket.Id, user.Id, user.Role, SupportTicketStatusResolved)
	assert.ErrorIs(t, err, ErrSupportTicketForbidden)
	_, err = UpdateSupportTicketStatus(ticket.Id, admin.Id, admin.Role, SupportTicketStatusResolved)
	require.NoError(t, err)

	_, err = AddSupportTicketMessage(ticket.Id, user.Id, user.Role, "The issue returned")
	require.NoError(t, err)
	detail, err = GetSupportTicketDetail(ticket.Id, user.Id, user.Role)
	require.NoError(t, err)
	assert.Equal(t, SupportTicketStatusOpen, detail.Ticket.Status)

	_, err = UpdateSupportTicketStatus(ticket.Id, user.Id, user.Role, SupportTicketStatusClosed)
	require.NoError(t, err)
	_, err = AddSupportTicketMessage(ticket.Id, user.Id, user.Role, "Reply after close")
	assert.ErrorIs(t, err, ErrSupportTicketClosed)

	_, err = UpdateSupportTicketStatus(ticket.Id, user.Id, user.Role, SupportTicketStatusOpen)
	require.NoError(t, err)
	_, err = AddSupportTicketMessage(ticket.Id, user.Id, user.Role, "Reply after reopen")
	require.NoError(t, err)

	detail, err = GetSupportTicketDetail(ticket.Id, user.Id, user.Role)
	require.NoError(t, err)
	require.Len(t, detail.Messages, 5)
	assert.Equal(t, SupportTicketStatusOpen, detail.Ticket.Status)
	assert.EqualValues(t, 5, detail.Ticket.MessageCount)
}

func TestListSupportTicketsFiltersByUsernameStatusAndPriority(t *testing.T) {
	db := setupSupportTicketTestDB(t)
	alice := createSupportTicketTestUser(t, db, "filter-alice", common.RoleCommonUser)
	bob := createSupportTicketTestUser(t, db, "filter-bob", common.RoleCommonUser)
	admin := createSupportTicketTestUser(t, db, "filter-admin", common.RoleAdminUser)

	aliceTicket := createSupportTicketFixture(t, alice, "Cannot call responses API")
	aliceTicket.Priority = SupportTicketPriorityUrgent
	require.NoError(t, db.Model(&SupportTicket{}).Where("id = ?", aliceTicket.Id).Update("priority", aliceTicket.Priority).Error)
	bobTicket := createSupportTicketFixture(t, bob, "Invoice question")
	_, err := UpdateSupportTicketStatus(bobTicket.Id, admin.Id, admin.Role, SupportTicketStatusResolved)
	require.NoError(t, err)

	items, total, err := ListSupportTickets(admin.Id, admin.Role, SupportTicketFilter{Keyword: "filter-alice"}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, aliceTicket.Id, items[0].Id)

	items, total, err = ListSupportTickets(admin.Id, admin.Role, SupportTicketFilter{
		Status: SupportTicketStatusResolved,
	}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, bobTicket.Id, items[0].Id)

	items, total, err = ListSupportTickets(admin.Id, admin.Role, SupportTicketFilter{
		Priority: SupportTicketPriorityUrgent,
	}, 0, 20)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, aliceTicket.Id, items[0].Id)

	_, _, err = ListSupportTickets(admin.Id, admin.Role, SupportTicketFilter{Status: "unknown"}, 0, 20)
	assert.True(t, errors.Is(err, ErrSupportTicketStatus))
}

func TestListSupportTicketsNormalizesNonPositiveLimit(t *testing.T) {
	db := setupSupportTicketTestDB(t)
	user := createSupportTicketTestUser(t, db, "ticket-page-size-user", common.RoleCommonUser)
	for index := 0; index < common.ItemsPerPage+1; index++ {
		createSupportTicketFixture(t, user, fmt.Sprintf("Ticket %d", index))
	}

	items, total, err := ListSupportTickets(user.Id, user.Role, SupportTicketFilter{}, 0, -1)
	require.NoError(t, err)
	assert.Len(t, items, common.ItemsPerPage)
	assert.EqualValues(t, common.ItemsPerPage+1, total)
}
