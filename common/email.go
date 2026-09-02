package common

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/smtp"
	"slices"
	"strings"
	"time"
)

var smtpSendTimeout = 15 * time.Second

func generateMessageID() (string, error) {
	return generateMessageIDFor(GetSMTPConfig().From)
}

func generateMessageIDFor(from string) (string, error) {
	split := strings.Split(from, "@")
	if len(split) < 2 {
		return "", fmt.Errorf("invalid SMTP account")
	}
	domain := split[1]
	return fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), GetRandomString(12), domain), nil
}

func shouldUseSMTPLoginAuth() bool {
	return shouldUseSMTPLoginAuthFor(GetSMTPConfig())
}

func shouldUseSMTPLoginAuthFor(cfg SMTPConfig) bool {
	if cfg.ForceAuthLogin {
		return true
	}
	return isOutlookServer(cfg.Account) || slices.Contains(EmailLoginAuthServerList, cfg.Server)
}

func getSMTPAuth() smtp.Auth {
	cfg := GetSMTPConfig()
	return autoSMTPAuthWithConfig(cfg.Account, cfg.Token, cfg.Server, cfg.ForceAuthLogin)
}

func shouldAuthenticateSMTP() bool {
	cfg := GetSMTPConfig()
	return cfg.Account != "" && cfg.Token != ""
}

func smtpTLSConfig() *tls.Config {
	return smtpTLSConfigFor(GetSMTPConfig())
}

func smtpTLSConfigFor(cfg SMTPConfig) *tls.Config {
	return &tls.Config{
		ServerName:         cfg.Server,
		InsecureSkipVerify: cfg.InsecureSkipVerify, // #nosec G402 -- admin-controlled SMTP compatibility option.
	}
}

func newSMTPClient(addr string) (*smtp.Client, error) {
	return newSMTPClientFor(addr, GetSMTPConfig())
}

func newSMTPClientFor(addr string, cfg SMTPConfig) (*smtp.Client, error) {
	if cfg.SSLEnabled || (cfg.Port == 465 && !cfg.StartTLSEnabled) {
		conn, err := dialSMTPConnection(addr)
		if err != nil {
			return nil, err
		}
		tlsConn := tls.Client(conn, smtpTLSConfigFor(cfg))
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return nil, err
		}
		client, err := smtp.NewClient(tlsConn, cfg.Server)
		if err != nil {
			_ = tlsConn.Close()
			return nil, err
		}
		return client, nil
	}

	conn, err := dialSMTPConnection(addr)
	if err != nil {
		return nil, err
	}
	client, err := smtp.NewClient(conn, cfg.Server)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	if cfg.StartTLSEnabled {
		startTLSSupported, _ := client.Extension("STARTTLS")
		if !startTLSSupported {
			_ = client.Close()
			return nil, fmt.Errorf("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(smtpTLSConfigFor(cfg)); err != nil {
			_ = client.Close()
			return nil, err
		}
	}

	return client, nil
}

func dialSMTPConnection(addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: smtpSendTimeout}).Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(smtpSendTimeout)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func SendEmail(subject string, receiver string, content string) error {
	cfg := GetSMTPConfig()
	from := cfg.From
	if from == "" { // for compatibility
		from = cfg.Account
	}
	id, err2 := generateMessageIDFor(from)
	if err2 != nil {
		return err2
	}
	if cfg.Server == "" && cfg.Account == "" {
		return fmt.Errorf("SMTP 服务器未配置")
	}
	encodedSubject := fmt.Sprintf("=?UTF-8?B?%s?=", base64.StdEncoding.EncodeToString([]byte(subject)))
	mail := []byte(fmt.Sprintf("To: %s\r\n"+
		"From: %s <%s>\r\n"+
		"Subject: %s\r\n"+
		"Date: %s\r\n"+
		"Message-ID: %s\r\n"+ // 添加 Message-ID 头
		"Content-Type: text/html; charset=UTF-8\r\n\r\n%s\r\n",
		receiver, cfg.SystemName, from, encodedSubject, time.Now().Format(time.RFC1123Z), id, content))
	auth := autoSMTPAuthWithConfig(cfg.Account, cfg.Token, cfg.Server, cfg.ForceAuthLogin)
	addr := fmt.Sprintf("%s:%d", cfg.Server, cfg.Port)
	to := strings.Split(receiver, ";")
	var err error
	client, err := newSMTPClientFor(addr, cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	if cfg.Account != "" && cfg.Token != "" {
		if err = client.Auth(auth); err != nil {
			return err
		}
	}
	if err = client.Mail(from); err != nil {
		return err
	}
	for _, receiver := range to {
		if err = client.Rcpt(receiver); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	_, err = w.Write(mail)
	if err != nil {
		return err
	}
	err = w.Close()
	if err != nil {
		return err
	}
	err = client.Quit()
	if err != nil {
		SysError(fmt.Sprintf("failed to send email to %s: %v", receiver, err))
	}
	return err
}
