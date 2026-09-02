package service

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/bytedance/gopkg/util/gopool"
)

const (
	supportTicketNotificationPollInterval  = 30 * time.Second
	supportTicketNotificationLeaseDuration = 5 * time.Minute
	supportTicketNotificationRetryBase     = 1 * time.Minute
	supportTicketNotificationRetryMax      = 1 * time.Hour
	supportTicketNotificationMaxPerPass    = 100
)

var (
	errSupportTicketNotificationRecipientUnavailable = errors.New("support ticket notification recipient is unavailable")
	supportTicketNotificationWorkerOnce              sync.Once
	supportTicketNotificationWakeup                  = make(chan struct{}, 1)
)

type supportTicketNotificationEmailSender func(subject string, recipient string, body string) error

type supportTicketNotificationWorker struct {
	id        string
	now       func() time.Time
	sendEmail supportTicketNotificationEmailSender
}

// NotifySupportTicketCreated only wakes the durable worker. The notification
// payload is already persisted by the ticket transaction, so request handling
// never performs best-effort SMTP work.
func NotifySupportTicketCreated(model.SupportTicket, string, string) {
	WakeSupportTicketNotificationWorker()
}

// WakeSupportTicketNotificationWorker wakes the durable notification worker
// after a ticket transaction commits. The worker reloads all notification data
// from the outbox, so the request path never waits for SMTP.
func WakeSupportTicketNotificationWorker() {
	select {
	case supportTicketNotificationWakeup <- struct{}{}:
	default:
	}
}

func StartSupportTicketNotificationWorker() {
	supportTicketNotificationWorkerOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}

		worker := supportTicketNotificationWorker{
			id:        "support-ticket-email-" + common.GetRandomString(16),
			now:       time.Now,
			sendEmail: common.SendEmail,
		}
		gopool.Go(func() {
			logger.LogInfo(context.Background(), fmt.Sprintf("support ticket notification worker started: poll_interval=%s", supportTicketNotificationPollInterval))
			ticker := time.NewTicker(supportTicketNotificationPollInterval)
			defer ticker.Stop()

			worker.runPass()
			for {
				select {
				case <-ticker.C:
				case <-supportTicketNotificationWakeup:
				}
				worker.runPass()
			}
		})
	})
}

func (worker supportTicketNotificationWorker) runPass() {
	for processedCount := 0; processedCount < supportTicketNotificationMaxPerPass; processedCount++ {
		processed, err := worker.processNext()
		if err != nil {
			logger.LogWarn(context.Background(), "support ticket notification worker pass stopped")
			return
		}
		if !processed {
			return
		}
	}
}

// processNext claims and processes at most one notification. Keeping this
// synchronous makes delivery sequential in production and deterministic in
// tests while the database lease provides at-least-once delivery.
func (worker supportTicketNotificationWorker) processNext() (bool, error) {
	if model.DB == nil {
		return false, errors.New("database is not initialized")
	}
	if strings.TrimSpace(worker.id) == "" {
		return false, errors.New("support ticket notification worker id is empty")
	}
	if worker.now == nil {
		return false, errors.New("support ticket notification clock is not configured")
	}

	now := worker.now()
	claimed, err := model.ClaimSupportTicketNotifications(
		worker.id,
		now.Unix(),
		now.Add(supportTicketNotificationLeaseDuration).Unix(),
		1,
	)
	if err != nil {
		return false, err
	}
	if len(claimed) == 0 {
		return false, nil
	}

	outbox := claimed[0]
	notificationContext, err := model.GetSupportTicketNotificationContext(outbox.TicketId)
	if err != nil {
		return true, worker.scheduleRetry(outbox, err)
	}

	subject, recipient, body, err := prepareSupportTicketNotification(
		notificationContext.Ticket,
		notificationContext.Username,
		notificationContext.Content,
	)
	if err != nil {
		return true, worker.scheduleRetry(outbox, err)
	}
	if recipient == "" {
		return true, worker.scheduleRetry(outbox, errSupportTicketNotificationRecipientUnavailable)
	}
	if worker.sendEmail == nil {
		return true, worker.scheduleRetry(outbox, errors.New("email sender is not configured"))
	}
	if err := worker.sendEmail(subject, recipient, body); err != nil {
		return true, worker.scheduleRetry(outbox, err)
	}

	if err := model.MarkSupportTicketNotificationSent(outbox.Id, worker.id, worker.now().Unix()); err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf("support ticket notification sent-state update failed: ticket_id=%d attempt=%d", outbox.TicketId, outbox.AttemptCount))
		return true, err
	}
	logger.LogInfo(context.Background(), fmt.Sprintf("support ticket notification sent: ticket_id=%d attempt=%d", outbox.TicketId, outbox.AttemptCount))
	return true, nil
}

func (worker supportTicketNotificationWorker) scheduleRetry(outbox model.SupportTicketNotificationOutbox, cause error) error {
	now := worker.now()
	nextAttemptAt := now.Add(supportTicketNotificationRetryDelay(outbox.AttemptCount)).Unix()
	if cause == nil {
		cause = errors.New("unknown notification failure")
	}
	if err := model.RetrySupportTicketNotification(outbox.Id, worker.id, now.Unix(), nextAttemptAt, cause.Error()); err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf("support ticket notification retry-state update failed: ticket_id=%d attempt=%d", outbox.TicketId, outbox.AttemptCount))
		return err
	}
	if outbox.AttemptCount >= model.SupportTicketNotificationMaxAttempts {
		logger.LogError(context.Background(), fmt.Sprintf("support ticket notification permanently failed: ticket_id=%d attempt=%d", outbox.TicketId, outbox.AttemptCount))
	} else {
		logger.LogWarn(context.Background(), fmt.Sprintf("support ticket notification failed; retry scheduled: ticket_id=%d attempt=%d", outbox.TicketId, outbox.AttemptCount))
	}
	return nil
}

func supportTicketNotificationRetryDelay(attemptCount int) time.Duration {
	delay := supportTicketNotificationRetryBase
	for attempt := 1; attempt < attemptCount && delay < supportTicketNotificationRetryMax; attempt++ {
		delay *= 2
		if delay >= supportTicketNotificationRetryMax {
			return supportTicketNotificationRetryMax
		}
	}
	return delay
}

func prepareSupportTicketNotification(ticket model.SupportTicket, username string, content string) (string, string, string, error) {
	if model.DB == nil {
		return "", "", "", errors.New("database is not initialized")
	}
	var root model.User
	result := model.DB.Select("id", "email", "setting").
		Where("role = ? AND status = ?", common.RoleRootUser, common.UserStatusEnabled).
		Order("id ASC").
		Limit(1).
		Find(&root)
	if result.Error != nil {
		return "", "", "", result.Error
	}
	if result.RowsAffected == 0 {
		return "", "", "", nil
	}

	recipient := strings.TrimSpace(root.GetSetting().NotificationEmail)
	if recipient == "" {
		recipient = strings.TrimSpace(root.Email)
	}
	if recipient == "" {
		return "", "", "", nil
	}

	subjectTitle := strings.NewReplacer("\r", " ", "\n", " ").Replace(ticket.Title)
	subject := fmt.Sprintf("[%s] 新工单 #%d: %s", common.GetSystemName(), ticket.Id, subjectTitle)
	body := formatSupportTicketNotificationHTML(ticket, username, content)
	return subject, recipient, body, nil
}

func formatSupportTicketNotificationHTML(ticket model.SupportTicket, username string, content string) string {
	return fmt.Sprintf(`<!doctype html>
<html><body>
<p>有新的用户工单提交，请及时处理。</p>
<table style="border-collapse:collapse">
<tr><td style="padding:4px 12px 4px 0"><strong>工单编号</strong></td><td>%d</td></tr>
<tr><td style="padding:4px 12px 4px 0"><strong>用户</strong></td><td>%s</td></tr>
<tr><td style="padding:4px 12px 4px 0"><strong>标题</strong></td><td>%s</td></tr>
<tr><td style="padding:4px 12px 4px 0"><strong>分类</strong></td><td>%s</td></tr>
<tr><td style="padding:4px 12px 4px 0"><strong>优先级</strong></td><td>%s</td></tr>
</table>
<p><strong>内容</strong></p>
<p>%s</p>
</body></html>`,
		ticket.Id,
		escapeSupportTicketHTML(username),
		escapeSupportTicketHTML(ticket.Title),
		escapeSupportTicketHTML(ticket.Category),
		escapeSupportTicketHTML(ticket.Priority),
		escapeSupportTicketHTML(content),
	)
}

func escapeSupportTicketHTML(value string) string {
	escaped := html.EscapeString(value)
	escaped = strings.ReplaceAll(escaped, "\r\n", "\n")
	return strings.ReplaceAll(escaped, "\n", "<br>")
}
