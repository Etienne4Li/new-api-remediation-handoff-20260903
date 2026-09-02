package model

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

type SupportTicketNotificationStatus string

const (
	SupportTicketNotificationStatusPending    SupportTicketNotificationStatus = "pending"
	SupportTicketNotificationStatusProcessing SupportTicketNotificationStatus = "processing"
	SupportTicketNotificationStatusSent       SupportTicketNotificationStatus = "sent"
	SupportTicketNotificationStatusFailed     SupportTicketNotificationStatus = "failed"

	SupportTicketNotificationMaxAttempts = 8
	maxSupportTicketNotificationErrorLen = 1024
)

var (
	ErrSupportTicketNotificationInvalidLease = errors.New("invalid support ticket notification lease")
	ErrSupportTicketNotificationLeaseLost    = errors.New("support ticket notification lease lost")
)

type SupportTicketNotificationOutbox struct {
	Id            int64                           `json:"id" gorm:"primaryKey"`
	TicketId      int                             `json:"ticket_id" gorm:"uniqueIndex"`
	Status        SupportTicketNotificationStatus `json:"status" gorm:"type:varchar(16);index:idx_support_ticket_notification_due,priority:1;index:idx_support_ticket_notification_lease,priority:1"`
	AttemptCount  int                             `json:"attempt_count"`
	NextAttemptAt int64                           `json:"next_attempt_at" gorm:"bigint;index:idx_support_ticket_notification_due,priority:2"`
	LockedBy      string                          `json:"locked_by" gorm:"type:varchar(128);index"`
	LockedUntil   int64                           `json:"locked_until" gorm:"bigint;index:idx_support_ticket_notification_lease,priority:2"`
	CreatedAt     int64                           `json:"created_at" gorm:"bigint"`
	UpdatedAt     int64                           `json:"updated_at" gorm:"bigint"`
	SentAt        int64                           `json:"sent_at" gorm:"bigint"`
	LastError     string                          `json:"last_error" gorm:"type:text"`
}

type SupportTicketNotificationContext struct {
	Ticket   SupportTicket
	Username string
	Content  string
}

func (SupportTicketNotificationOutbox) TableName() string {
	return "support_ticket_notification_outboxes"
}

func createSupportTicketNotificationOutbox(tx *gorm.DB, ticketId int, now int64) error {
	outbox := &SupportTicketNotificationOutbox{
		TicketId:      ticketId,
		Status:        SupportTicketNotificationStatusPending,
		NextAttemptAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	return tx.Create(outbox).Error
}

// ClaimSupportTicketNotifications leases due rows. Expired leases are
// reclaimed to provide at-least-once delivery; callers must make delivery
// idempotent because a process can exit after SMTP accepts a message.
func ClaimSupportTicketNotifications(workerId string, now int64, lockUntil int64, limit int) ([]SupportTicketNotificationOutbox, error) {
	workerId = strings.TrimSpace(workerId)
	if DB == nil || workerId == "" || lockUntil <= now {
		return nil, ErrSupportTicketNotificationInvalidLease
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}

	candidateLimit := limit * 4
	if candidateLimit > 400 {
		candidateLimit = 400
	}
	var candidates []SupportTicketNotificationOutbox
	if err := dueSupportTicketNotifications(DB, now).
		Order("id ASC").
		Limit(candidateLimit).
		Find(&candidates).Error; err != nil {
		return nil, err
	}

	claimed := make([]SupportTicketNotificationOutbox, 0, limit)
	for _, candidate := range candidates {
		if candidate.AttemptCount >= SupportTicketNotificationMaxAttempts {
			if err := markSupportTicketNotificationFailed(candidate.Id, "maximum notification attempts exceeded", now); err != nil {
				return nil, err
			}
			continue
		}

		result := dueSupportTicketNotifications(DB.Model(&SupportTicketNotificationOutbox{}).Where("id = ?", candidate.Id), now).
			Where("attempt_count < ?", SupportTicketNotificationMaxAttempts).
			Updates(map[string]any{
				"status":        SupportTicketNotificationStatusProcessing,
				"attempt_count": gorm.Expr("attempt_count + ?", 1),
				"locked_by":     workerId,
				"locked_until":  lockUntil,
				"updated_at":    now,
			})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}

		candidate.Status = SupportTicketNotificationStatusProcessing
		candidate.AttemptCount++
		candidate.LockedBy = workerId
		candidate.LockedUntil = lockUntil
		candidate.UpdatedAt = now
		claimed = append(claimed, candidate)
		if len(claimed) == limit {
			break
		}
	}
	return claimed, nil
}

// RetrySupportTicketNotification records a failed attempt. The variadic error
// argument preserves the old call shape for integrations while allowing the
// worker to persist a bounded diagnostic message.
func RetrySupportTicketNotification(outboxId int64, workerId string, now int64, nextAttemptAt int64, failure ...string) error {
	if DB == nil {
		return ErrSupportTicketNotificationLeaseLost
	}
	if nextAttemptAt < now {
		nextAttemptAt = now
	}
	var current SupportTicketNotificationOutbox
	lease := currentSupportTicketNotificationLease(outboxId, workerId, now)
	if err := lease.First(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSupportTicketNotificationLeaseLost
		}
		return err
	}

	status := SupportTicketNotificationStatusPending
	if current.AttemptCount >= SupportTicketNotificationMaxAttempts {
		status = SupportTicketNotificationStatusFailed
		nextAttemptAt = 0
	}
	lastError := ""
	if len(failure) > 0 {
		lastError = strings.TrimSpace(failure[0])
	}
	if len(lastError) > maxSupportTicketNotificationErrorLen {
		lastError = lastError[:maxSupportTicketNotificationErrorLen]
	}

	result := currentSupportTicketNotificationLease(outboxId, workerId, now).
		Updates(map[string]any{
			"status":          status,
			"next_attempt_at": nextAttemptAt,
			"locked_by":       "",
			"locked_until":    0,
			"updated_at":      now,
			"last_error":      lastError,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSupportTicketNotificationLeaseLost
	}
	return nil
}

func MarkSupportTicketNotificationSent(outboxId int64, workerId string, now int64) error {
	result := currentSupportTicketNotificationLease(outboxId, workerId, now).
		Updates(map[string]any{
			"status":       SupportTicketNotificationStatusSent,
			"locked_by":    "",
			"locked_until": 0,
			"sent_at":      now,
			"updated_at":   now,
			"last_error":   "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSupportTicketNotificationLeaseLost
	}
	return nil
}

func GetSupportTicketNotificationContext(ticketId int) (*SupportTicketNotificationContext, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	var ticket SupportTicket
	if err := DB.Where("id = ?", ticketId).First(&ticket).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSupportTicketNotFound
		}
		return nil, err
	}

	var user struct {
		Username string
	}
	if err := DB.Model(&User{}).Select("username").Where("id = ?", ticket.UserId).First(&user).Error; err != nil {
		return nil, err
	}

	var message SupportTicketMessage
	if err := DB.Where("ticket_id = ?", ticketId).Order("created_time ASC, id ASC").First(&message).Error; err != nil {
		return nil, err
	}
	return &SupportTicketNotificationContext{
		Ticket:   ticket,
		Username: user.Username,
		Content:  message.Content,
	}, nil
}

func dueSupportTicketNotifications(db *gorm.DB, now int64) *gorm.DB {
	return db.Where(
		"(status = ? AND next_attempt_at <= ?) OR (status = ? AND locked_until < ?)",
		SupportTicketNotificationStatusPending,
		now,
		SupportTicketNotificationStatusProcessing,
		now,
	)
}

func currentSupportTicketNotificationLease(outboxId int64, workerId string, now int64) *gorm.DB {
	return DB.Model(&SupportTicketNotificationOutbox{}).
		Where("id = ? AND status = ? AND locked_by = ? AND locked_until >= ?", outboxId, SupportTicketNotificationStatusProcessing, strings.TrimSpace(workerId), now)
}

func markSupportTicketNotificationFailed(outboxId int64, reason string, now int64) error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	result := DB.Model(&SupportTicketNotificationOutbox{}).
		Where("id = ? AND status <> ?", outboxId, SupportTicketNotificationStatusSent).
		Updates(map[string]any{
			"status":          SupportTicketNotificationStatusFailed,
			"next_attempt_at": 0,
			"locked_by":       "",
			"locked_until":    0,
			"updated_at":      now,
			"last_error":      strings.TrimSpace(reason),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("notification %d was not marked failed", outboxId)
	}
	return nil
}
