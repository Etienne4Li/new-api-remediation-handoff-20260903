package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupSupportTicketControllerTestDB(t *testing.T) *gorm.DB {
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

func createSupportTicketControllerUser(t *testing.T, db *gorm.DB, username string, role int) model.User {
	t.Helper()
	user := model.User{
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

func performSupportTicketHandler(
	t *testing.T,
	method string,
	path string,
	body string,
	user model.User,
	params gin.Params,
	handler gin.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	context.Params = params
	context.Set("id", user.Id)
	context.Set("role", user.Role)
	context.Set("username", user.Username)
	handler(context)
	return recorder
}

func TestCreateSupportTicketValidatesInputAndReturnsConversation(t *testing.T) {
	db := setupSupportTicketControllerTestDB(t)
	user := createSupportTicketControllerUser(t, db, "controller-ticket-user", common.RoleCommonUser)

	invalid := performSupportTicketHandler(
		t,
		http.MethodPost,
		"/api/ticket/",
		`{"title":"","category":"api","priority":"normal","content":"Need help"}`,
		user,
		nil,
		CreateSupportTicket,
	)
	assert.Equal(t, http.StatusOK, invalid.Code)
	assert.Contains(t, invalid.Body.String(), `"success":false`)
	var count int64
	require.NoError(t, db.Model(&model.SupportTicket{}).Count(&count).Error)
	assert.Zero(t, count)

	valid := performSupportTicketHandler(
		t,
		http.MethodPost,
		"/api/ticket/",
		`{"title":"Responses API error","category":"api","priority":"high","content":"Requests return 503"}`,
		user,
		nil,
		CreateSupportTicket,
	)
	assert.Equal(t, http.StatusOK, valid.Code)
	var response struct {
		Success bool                      `json:"success"`
		Data    model.SupportTicketDetail `json:"data"`
	}
	require.NoError(t, common.Unmarshal(valid.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, "Responses API error", response.Data.Ticket.Title)
	assert.Equal(t, model.SupportTicketStatusOpen, response.Data.Ticket.Status)
	require.Len(t, response.Data.Messages, 1)
	assert.Equal(t, "Requests return 503", response.Data.Messages[0].Content)
	assert.Equal(t, user.Username, response.Data.Messages[0].AuthorName)
}

func TestCreateSupportTicketNotifiesOnlyAfterTransactionCommits(t *testing.T) {
	db := setupSupportTicketControllerTestDB(t)
	user := createSupportTicketControllerUser(t, db, "controller-ticket-notifier", common.RoleCommonUser)
	previousNotifier := supportTicketCreatedNotifier
	t.Cleanup(func() { supportTicketCreatedNotifier = previousNotifier })

	var notifiedTicket model.SupportTicket
	var notifiedUsername string
	var notifiedContent string
	supportTicketCreatedNotifier = func(ticket model.SupportTicket, username string, content string) {
		notifiedTicket = ticket
		notifiedUsername = username
		notifiedContent = content
		var persisted model.SupportTicket
		require.NoError(t, db.First(&persisted, ticket.Id).Error)
		assert.Equal(t, model.SupportTicketStatusOpen, persisted.Status)
	}

	response := performSupportTicketHandler(
		t,
		http.MethodPost,
		"/api/ticket/",
		`{"title":"Notify after commit","category":"api","priority":"normal","content":"Please investigate"}`,
		user,
		nil,
		CreateSupportTicket,
	)

	var result struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success)
	assert.NotZero(t, notifiedTicket.Id)
	assert.Equal(t, user.Username, notifiedUsername)
	assert.Equal(t, "Please investigate", notifiedContent)
}

func TestCreateSupportTicketDoesNotNotifyWhenCreatedByAdmin(t *testing.T) {
	db := setupSupportTicketControllerTestDB(t)
	admin := createSupportTicketControllerUser(t, db, "controller-ticket-admin-no-notify", common.RoleAdminUser)
	previousNotifier := supportTicketCreatedNotifier
	t.Cleanup(func() { supportTicketCreatedNotifier = previousNotifier })

	notified := false
	supportTicketCreatedNotifier = func(model.SupportTicket, string, string) {
		notified = true
	}

	response := performSupportTicketHandler(
		t,
		http.MethodPost,
		"/api/ticket/",
		`{"title":"Admin internal ticket","category":"api","priority":"normal","content":"Internal check"}`,
		admin,
		nil,
		CreateSupportTicket,
	)

	var result struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success)
	assert.False(t, notified)
	var count int64
	require.NoError(t, db.Model(&model.SupportTicket{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestCreateSupportTicketValidatesTitleRuneBoundaries(t *testing.T) {
	testCases := []struct {
		name        string
		titleLength int
		wantSuccess bool
	}{
		{name: "four runes", titleLength: 4, wantSuccess: false},
		{name: "five runes", titleLength: 5, wantSuccess: true},
		{name: "one hundred twenty runes", titleLength: 120, wantSuccess: true},
		{name: "one hundred twenty-one runes", titleLength: 121, wantSuccess: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			db := setupSupportTicketControllerTestDB(t)
			user := createSupportTicketControllerUser(t, db, "title-boundary-user", common.RoleCommonUser)
			title := strings.Repeat("工", testCase.titleLength)
			response := performSupportTicketHandler(
				t,
				http.MethodPost,
				"/api/ticket/",
				fmt.Sprintf(`{"title":"%s","category":"api","priority":"normal","content":"Need help"}`, title),
				user,
				nil,
				CreateSupportTicket,
			)

			var result struct {
				Success bool `json:"success"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(t, testCase.wantSuccess, result.Success)

			var count int64
			require.NoError(t, db.Model(&model.SupportTicket{}).Count(&count).Error)
			if testCase.wantSuccess {
				assert.EqualValues(t, 1, count)
			} else {
				assert.Zero(t, count)
			}
		})
	}
}

func TestListSupportTicketsNormalizesNegativePageSize(t *testing.T) {
	db := setupSupportTicketControllerTestDB(t)
	user := createSupportTicketControllerUser(t, db, "negative-page-size-user", common.RoleCommonUser)

	response := performSupportTicketHandler(
		t,
		http.MethodGet,
		"/api/ticket/?page_size=-1",
		"",
		user,
		nil,
		ListSupportTickets,
	)

	var result struct {
		Success bool `json:"success"`
		Data    struct {
			PageSize int `json:"page_size"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success)
	assert.Equal(t, common.ItemsPerPage, result.Data.PageSize)
}

func TestSupportTicketHandlersDoNotExposeOtherUsersTickets(t *testing.T) {
	db := setupSupportTicketControllerTestDB(t)
	owner := createSupportTicketControllerUser(t, db, "controller-ticket-owner", common.RoleCommonUser)
	other := createSupportTicketControllerUser(t, db, "controller-ticket-other", common.RoleCommonUser)
	admin := createSupportTicketControllerUser(t, db, "controller-ticket-admin", common.RoleAdminUser)
	ticket := model.SupportTicket{
		UserId:   owner.Id,
		Title:    "Private billing question",
		Category: model.SupportTicketCategoryBilling,
		Priority: model.SupportTicketPriorityNormal,
	}
	require.NoError(t, model.CreateSupportTicket(&ticket, owner.Role, "Private account details"))
	params := gin.Params{{Key: "id", Value: fmt.Sprintf("%d", ticket.Id)}}

	otherResponse := performSupportTicketHandler(t, http.MethodGet, "/api/ticket/1", "", other, params, GetSupportTicket)
	assert.Contains(t, otherResponse.Body.String(), `"success":false`)
	assert.NotContains(t, otherResponse.Body.String(), "Private account details")

	ownerResolve := performSupportTicketHandler(
		t,
		http.MethodPatch,
		"/api/ticket/1/status",
		`{"status":"resolved"}`,
		owner,
		params,
		UpdateSupportTicketStatus,
	)
	assert.Contains(t, ownerResolve.Body.String(), `"success":false`)

	adminResolve := performSupportTicketHandler(
		t,
		http.MethodPatch,
		"/api/ticket/1/status",
		`{"status":"resolved"}`,
		admin,
		params,
		UpdateSupportTicketStatus,
	)
	assert.Contains(t, adminResolve.Body.String(), `"success":true`)
	assert.Contains(t, adminResolve.Body.String(), `"status":"resolved"`)
}
