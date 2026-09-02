package model

import (
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

const (
	SupportTicketCategoryAccount = "account"
	SupportTicketCategoryBilling = "billing"
	SupportTicketCategoryAPI     = "api"
	SupportTicketCategoryOther   = "other"

	SupportTicketPriorityLow    = "low"
	SupportTicketPriorityNormal = "normal"
	SupportTicketPriorityHigh   = "high"
	SupportTicketPriorityUrgent = "urgent"

	SupportTicketStatusOpen       = "open"
	SupportTicketStatusInProgress = "in_progress"
	SupportTicketStatusResolved   = "resolved"
	SupportTicketStatusClosed     = "closed"

	SupportTicketReplyByUser  = "user"
	SupportTicketReplyByAdmin = "admin"
)

var (
	ErrSupportTicketNotFound  = errors.New("support ticket not found")
	ErrSupportTicketForbidden = errors.New("support ticket access denied")
	ErrSupportTicketClosed    = errors.New("support ticket is closed")
	ErrSupportTicketStatus    = errors.New("invalid support ticket status")
	ErrSupportTicketCategory  = errors.New("invalid support ticket category")
	ErrSupportTicketPriority  = errors.New("invalid support ticket priority")
)

type SupportTicket struct {
	Id            int    `json:"id"`
	UserId        int    `json:"user_id" gorm:"index:idx_support_tickets_user_updated,priority:1"`
	Title         string `json:"title" gorm:"type:varchar(120)"`
	Category      string `json:"category" gorm:"type:varchar(32);index"`
	Priority      string `json:"priority" gorm:"type:varchar(16);index"`
	Status        string `json:"status" gorm:"type:varchar(24);index"`
	CreatedTime   int64  `json:"created_time" gorm:"bigint"`
	UpdatedTime   int64  `json:"updated_time" gorm:"bigint;index;index:idx_support_tickets_user_updated,priority:2"`
	LastReplyTime int64  `json:"last_reply_time" gorm:"bigint"`
	LastReplyBy   string `json:"last_reply_by" gorm:"type:varchar(16)"`
}

type SupportTicketMessage struct {
	Id          int    `json:"id"`
	TicketId    int    `json:"ticket_id" gorm:"index:idx_support_ticket_messages_ticket_created,priority:1"`
	AuthorId    int    `json:"author_id" gorm:"index"`
	AuthorRole  int    `json:"author_role"`
	Content     string `json:"content" gorm:"type:text"`
	CreatedTime int64  `json:"created_time" gorm:"bigint;index:idx_support_ticket_messages_ticket_created,priority:2"`
}

type SupportTicketListItem struct {
	SupportTicket
	Username     string `json:"username"`
	MessageCount int64  `json:"message_count"`
}

type SupportTicketMessageView struct {
	SupportTicketMessage
	AuthorName string `json:"author_name"`
}

type SupportTicketDetail struct {
	Ticket   SupportTicketListItem      `json:"ticket"`
	Messages []SupportTicketMessageView `json:"messages"`
}

type SupportTicketFilter struct {
	Keyword  string
	Status   string
	Priority string
	Category string
}

func (SupportTicket) TableName() string {
	return "support_tickets"
}

func (SupportTicketMessage) TableName() string {
	return "support_ticket_messages"
}

func IsSupportTicketCategory(category string) bool {
	switch category {
	case SupportTicketCategoryAccount, SupportTicketCategoryBilling, SupportTicketCategoryAPI, SupportTicketCategoryOther:
		return true
	default:
		return false
	}
}

func IsSupportTicketPriority(priority string) bool {
	switch priority {
	case SupportTicketPriorityLow, SupportTicketPriorityNormal, SupportTicketPriorityHigh, SupportTicketPriorityUrgent:
		return true
	default:
		return false
	}
}

func IsSupportTicketStatus(status string) bool {
	switch status {
	case SupportTicketStatusOpen, SupportTicketStatusInProgress, SupportTicketStatusResolved, SupportTicketStatusClosed:
		return true
	default:
		return false
	}
}

func CreateSupportTicket(ticket *SupportTicket, authorRole int, content string) error {
	if ticket == nil || ticket.UserId <= 0 {
		return ErrSupportTicketForbidden
	}
	if !IsSupportTicketCategory(ticket.Category) {
		return ErrSupportTicketCategory
	}
	if !IsSupportTicketPriority(ticket.Priority) {
		return ErrSupportTicketPriority
	}

	now := common.GetTimestamp()
	ticket.Title = strings.TrimSpace(ticket.Title)
	ticket.Status = SupportTicketStatusOpen
	ticket.CreatedTime = now
	ticket.UpdatedTime = now
	ticket.LastReplyTime = now
	ticket.LastReplyBy = SupportTicketReplyByUser

	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ticket).Error; err != nil {
			return err
		}
		message := SupportTicketMessage{
			TicketId:    ticket.Id,
			AuthorId:    ticket.UserId,
			AuthorRole:  authorRole,
			Content:     strings.TrimSpace(content),
			CreatedTime: now,
		}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		if authorRole >= common.RoleAdminUser {
			return nil
		}
		return createSupportTicketNotificationOutbox(tx, ticket.Id, now)
	})
}

func ListSupportTickets(actorId int, actorRole int, filter SupportTicketFilter, startIdx int, num int) ([]SupportTicketListItem, int64, error) {
	if num < 1 {
		num = common.ItemsPerPage
	}
	query := DB.Model(&SupportTicket{})
	if actorRole < common.RoleAdminUser {
		query = query.Where("support_tickets.user_id = ?", actorId)
	}

	keyword := strings.TrimSpace(filter.Keyword)
	if keyword != "" {
		like := "%" + keyword + "%"
		query = query.Joins("LEFT JOIN users ON users.id = support_tickets.user_id")
		if id, err := strconv.Atoi(keyword); err == nil {
			query = query.Where("support_tickets.id = ? OR support_tickets.title LIKE ? OR users.username LIKE ?", id, like, like)
		} else {
			query = query.Where("support_tickets.title LIKE ? OR users.username LIKE ?", like, like)
		}
	}
	if filter.Status != "" {
		if !IsSupportTicketStatus(filter.Status) {
			return nil, 0, ErrSupportTicketStatus
		}
		query = query.Where("support_tickets.status = ?", filter.Status)
	}
	if filter.Priority != "" {
		if !IsSupportTicketPriority(filter.Priority) {
			return nil, 0, ErrSupportTicketPriority
		}
		query = query.Where("support_tickets.priority = ?", filter.Priority)
	}
	if filter.Category != "" {
		if !IsSupportTicketCategory(filter.Category) {
			return nil, 0, ErrSupportTicketCategory
		}
		query = query.Where("support_tickets.category = ?", filter.Category)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var tickets []SupportTicket
	if err := query.Select("support_tickets.*").Order("support_tickets.updated_time DESC, support_tickets.id DESC").Limit(num).Offset(startIdx).Find(&tickets).Error; err != nil {
		return nil, 0, err
	}
	items, err := enrichSupportTicketList(tickets)
	return items, total, err
}

func GetSupportTicketDetail(ticketId int, actorId int, actorRole int) (*SupportTicketDetail, error) {
	var ticket SupportTicket
	query := DB.Where("id = ?", ticketId)
	if actorRole < common.RoleAdminUser {
		query = query.Where("user_id = ?", actorId)
	}
	if err := query.First(&ticket).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSupportTicketNotFound
		}
		return nil, err
	}

	items, err := enrichSupportTicketList([]SupportTicket{ticket})
	if err != nil {
		return nil, err
	}

	var messages []SupportTicketMessage
	if err := DB.Where("ticket_id = ?", ticketId).Order("created_time ASC, id ASC").Find(&messages).Error; err != nil {
		return nil, err
	}
	messageViews, err := enrichSupportTicketMessages(messages)
	if err != nil {
		return nil, err
	}
	return &SupportTicketDetail{Ticket: items[0], Messages: messageViews}, nil
}

func AddSupportTicketMessage(ticketId int, actorId int, actorRole int, content string) (*SupportTicketMessage, error) {
	message := &SupportTicketMessage{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var ticket SupportTicket
		query := lockForUpdate(tx).Where("id = ?", ticketId)
		if actorRole < common.RoleAdminUser {
			query = query.Where("user_id = ?", actorId)
		}
		if err := query.First(&ticket).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSupportTicketNotFound
			}
			return err
		}
		if ticket.Status == SupportTicketStatusClosed {
			return ErrSupportTicketClosed
		}

		now := common.GetTimestamp()
		nextStatus := SupportTicketStatusOpen
		lastReplyBy := SupportTicketReplyByUser
		if actorRole >= common.RoleAdminUser {
			nextStatus = SupportTicketStatusInProgress
			lastReplyBy = SupportTicketReplyByAdmin
		}
		message = &SupportTicketMessage{
			TicketId:    ticket.Id,
			AuthorId:    actorId,
			AuthorRole:  actorRole,
			Content:     strings.TrimSpace(content),
			CreatedTime: now,
		}
		if err := tx.Create(message).Error; err != nil {
			return err
		}
		return tx.Model(&SupportTicket{}).Where("id = ?", ticket.Id).Updates(map[string]interface{}{
			"status":          nextStatus,
			"updated_time":    now,
			"last_reply_time": now,
			"last_reply_by":   lastReplyBy,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return message, nil
}

func UpdateSupportTicketStatus(ticketId int, actorId int, actorRole int, status string) (*SupportTicket, error) {
	if !IsSupportTicketStatus(status) {
		return nil, ErrSupportTicketStatus
	}

	var ticket SupportTicket
	err := DB.Transaction(func(tx *gorm.DB) error {
		query := lockForUpdate(tx).Where("id = ?", ticketId)
		if actorRole < common.RoleAdminUser {
			query = query.Where("user_id = ?", actorId)
		}
		if err := query.First(&ticket).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSupportTicketNotFound
			}
			return err
		}
		if ticket.Status == status {
			return nil
		}
		if actorRole < common.RoleAdminUser {
			canClose := status == SupportTicketStatusClosed
			canReopen := status == SupportTicketStatusOpen && (ticket.Status == SupportTicketStatusClosed || ticket.Status == SupportTicketStatusResolved)
			if !canClose && !canReopen {
				return ErrSupportTicketForbidden
			}
		}

		ticket.Status = status
		ticket.UpdatedTime = common.GetTimestamp()
		return tx.Model(&SupportTicket{}).Where("id = ?", ticket.Id).Updates(map[string]interface{}{
			"status":       ticket.Status,
			"updated_time": ticket.UpdatedTime,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return &ticket, nil
}

func enrichSupportTicketList(tickets []SupportTicket) ([]SupportTicketListItem, error) {
	items := make([]SupportTicketListItem, len(tickets))
	if len(tickets) == 0 {
		return items, nil
	}

	userIds := make([]int, 0, len(tickets))
	ticketIds := make([]int, 0, len(tickets))
	for i, ticket := range tickets {
		items[i].SupportTicket = ticket
		userIds = append(userIds, ticket.UserId)
		ticketIds = append(ticketIds, ticket.Id)
	}

	var users []struct {
		Id       int
		Username string
	}
	if err := DB.Model(&User{}).Select("id", "username").Where("id IN ?", userIds).Find(&users).Error; err != nil {
		return nil, err
	}
	usernames := make(map[int]string, len(users))
	for _, user := range users {
		usernames[user.Id] = user.Username
	}

	var counts []struct {
		TicketId int
		Count    int64
	}
	if err := DB.Model(&SupportTicketMessage{}).Select("ticket_id", "COUNT(*) AS count").Where("ticket_id IN ?", ticketIds).Group("ticket_id").Scan(&counts).Error; err != nil {
		return nil, err
	}
	messageCounts := make(map[int]int64, len(counts))
	for _, count := range counts {
		messageCounts[count.TicketId] = count.Count
	}

	for i := range items {
		items[i].Username = usernames[items[i].UserId]
		items[i].MessageCount = messageCounts[items[i].Id]
	}
	return items, nil
}

func enrichSupportTicketMessages(messages []SupportTicketMessage) ([]SupportTicketMessageView, error) {
	views := make([]SupportTicketMessageView, len(messages))
	if len(messages) == 0 {
		return views, nil
	}

	authorIds := make([]int, 0, len(messages))
	for i, message := range messages {
		views[i].SupportTicketMessage = message
		authorIds = append(authorIds, message.AuthorId)
	}
	var users []struct {
		Id       int
		Username string
	}
	if err := DB.Model(&User{}).Select("id", "username").Where("id IN ?", authorIds).Find(&users).Error; err != nil {
		return nil, err
	}
	usernames := make(map[int]string, len(users))
	for _, user := range users {
		usernames[user.Id] = user.Username
	}
	for i := range views {
		views[i].AuthorName = usernames[views[i].AuthorId]
	}
	return views, nil
}
