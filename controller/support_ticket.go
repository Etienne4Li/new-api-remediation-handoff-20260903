package controller

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const (
	minSupportTicketTitleLength   = 5
	maxSupportTicketTitleLength   = 120
	maxSupportTicketMessageLength = 5000
)

// Keep the notifier injectable so the ticket handler can verify the
// post-transaction side effect without starting an SMTP worker in tests.
var supportTicketCreatedNotifier = service.NotifySupportTicketCreated

type createSupportTicketRequest struct {
	Title    string `json:"title"`
	Category string `json:"category"`
	Priority string `json:"priority"`
	Content  string `json:"content"`
}

type supportTicketMessageRequest struct {
	Content string `json:"content"`
}

type supportTicketStatusRequest struct {
	Status string `json:"status"`
}

func ListSupportTickets(c *gin.Context) {
	filter := model.SupportTicketFilter{
		Keyword:  c.Query("keyword"),
		Status:   c.Query("status"),
		Priority: c.Query("priority"),
		Category: c.Query("category"),
	}
	pageInfo := common.GetPageQuery(c)
	if pageInfo.PageSize < 1 {
		pageInfo.PageSize = common.ItemsPerPage
	}
	items, total, err := model.ListSupportTickets(c.GetInt("id"), c.GetInt("role"), filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		writeSupportTicketError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

func GetSupportTicket(c *gin.Context) {
	ticketId, ok := parseSupportTicketId(c)
	if !ok {
		return
	}
	detail, err := model.GetSupportTicketDetail(ticketId, c.GetInt("id"), c.GetInt("role"))
	if err != nil {
		writeSupportTicketError(c, err)
		return
	}
	common.ApiSuccess(c, detail)
}

func CreateSupportTicket(c *gin.Context) {
	var request createSupportTicketRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	request.Title = strings.TrimSpace(request.Title)
	request.Content = strings.TrimSpace(request.Content)
	titleLength := utf8.RuneCountInString(request.Title)
	if titleLength < minSupportTicketTitleLength || titleLength > maxSupportTicketTitleLength {
		common.ApiErrorI18n(c, i18n.MsgTicketTitleLength, map[string]any{
			"Min": minSupportTicketTitleLength,
			"Max": maxSupportTicketTitleLength,
		})
		return
	}
	if utf8.RuneCountInString(request.Content) == 0 || utf8.RuneCountInString(request.Content) > maxSupportTicketMessageLength {
		common.ApiErrorI18n(c, i18n.MsgTicketMessageLength, map[string]any{"Max": maxSupportTicketMessageLength})
		return
	}
	if !model.IsSupportTicketCategory(request.Category) {
		common.ApiErrorI18n(c, i18n.MsgTicketCategoryInvalid)
		return
	}
	if !model.IsSupportTicketPriority(request.Priority) {
		common.ApiErrorI18n(c, i18n.MsgTicketPriorityInvalid)
		return
	}

	ticket := &model.SupportTicket{
		UserId:   c.GetInt("id"),
		Title:    request.Title,
		Category: request.Category,
		Priority: request.Priority,
	}
	if err := model.CreateSupportTicket(ticket, c.GetInt("role"), request.Content); err != nil {
		writeSupportTicketError(c, err)
		return
	}
	if c.GetInt("role") < common.RoleAdminUser {
		supportTicketCreatedNotifier(*ticket, c.GetString("username"), request.Content)
	}
	detail, err := model.GetSupportTicketDetail(ticket.Id, c.GetInt("id"), c.GetInt("role"))
	if err != nil {
		writeSupportTicketError(c, err)
		return
	}
	common.ApiSuccess(c, detail)
}

func AddSupportTicketMessage(c *gin.Context) {
	ticketId, ok := parseSupportTicketId(c)
	if !ok {
		return
	}
	var request supportTicketMessageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	request.Content = strings.TrimSpace(request.Content)
	if utf8.RuneCountInString(request.Content) == 0 || utf8.RuneCountInString(request.Content) > maxSupportTicketMessageLength {
		common.ApiErrorI18n(c, i18n.MsgTicketMessageLength, map[string]any{"Max": maxSupportTicketMessageLength})
		return
	}
	if _, err := model.AddSupportTicketMessage(ticketId, c.GetInt("id"), c.GetInt("role"), request.Content); err != nil {
		writeSupportTicketError(c, err)
		return
	}
	detail, err := model.GetSupportTicketDetail(ticketId, c.GetInt("id"), c.GetInt("role"))
	if err != nil {
		writeSupportTicketError(c, err)
		return
	}
	common.ApiSuccess(c, detail)
}

func UpdateSupportTicketStatus(c *gin.Context) {
	ticketId, ok := parseSupportTicketId(c)
	if !ok {
		return
	}
	var request supportTicketStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || !model.IsSupportTicketStatus(request.Status) {
		common.ApiErrorI18n(c, i18n.MsgTicketStatusInvalid)
		return
	}
	if _, err := model.UpdateSupportTicketStatus(ticketId, c.GetInt("id"), c.GetInt("role"), request.Status); err != nil {
		writeSupportTicketError(c, err)
		return
	}
	detail, err := model.GetSupportTicketDetail(ticketId, c.GetInt("id"), c.GetInt("role"))
	if err != nil {
		writeSupportTicketError(c, err)
		return
	}
	common.ApiSuccess(c, detail)
}

func parseSupportTicketId(c *gin.Context) (int, bool) {
	ticketId, err := strconv.Atoi(c.Param("id"))
	if err != nil || ticketId <= 0 {
		common.ApiErrorI18n(c, i18n.MsgInvalidId)
		return 0, false
	}
	return ticketId, true
}

func writeSupportTicketError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrSupportTicketNotFound):
		common.ApiErrorI18n(c, i18n.MsgTicketNotFound)
	case errors.Is(err, model.ErrSupportTicketForbidden):
		common.ApiErrorI18n(c, i18n.MsgForbidden)
	case errors.Is(err, model.ErrSupportTicketClosed):
		common.ApiErrorI18n(c, i18n.MsgTicketClosed)
	case errors.Is(err, model.ErrSupportTicketStatus):
		common.ApiErrorI18n(c, i18n.MsgTicketStatusInvalid)
	case errors.Is(err, model.ErrSupportTicketCategory):
		common.ApiErrorI18n(c, i18n.MsgTicketCategoryInvalid)
	case errors.Is(err, model.ErrSupportTicketPriority):
		common.ApiErrorI18n(c, i18n.MsgTicketPriorityInvalid)
	default:
		common.SysError("support ticket operation failed: " + err.Error())
		common.ApiErrorI18n(c, i18n.MsgDatabaseError)
	}
}
