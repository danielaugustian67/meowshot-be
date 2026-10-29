// Package mail sends transactional email (password reset codes).
package mail

import (
	"fmt"
	"log/slog"
	"net/mail"
	"net/smtp"
	"strings"

	"meowshot/internal/config"
)

type Sender interface {
	Send(to, subject, body string) error
}

// New returns an SMTP sender, or a logger when SMTP_HOST is empty (local development).
func New(cfg config.SMTPConfig, log *slog.Logger) Sender {
	if cfg.Host == "" {
		return logSender{log: log}
	}
	return &smtpSender{cfg: cfg}
}

type smtpSender struct{ cfg config.SMTPConfig }

func (s *smtpSender) Send(to, subject, body string) error {
	from, err := mail.ParseAddress(s.cfg.From)
	if err != nil {
		return fmt.Errorf("invalid SMTP_FROM: %w", err)
	}
	if strings.ContainsAny(to+subject, "\r\n") {
		return fmt.Errorf("mail: header injection rejected")
	}
	msg := "From: " + s.cfg.From + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" + body
	var auth smtp.Auth
	if s.cfg.Username != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	}
	// smtp.SendMail upgrades to STARTTLS when the server offers it.
	return smtp.SendMail(fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port), auth, from.Address, []string{to}, []byte(msg))
}

type logSender struct{ log *slog.Logger }

func (l logSender) Send(to, subject, body string) error {
	l.log.Info("email (SMTP not configured)", "to", to, "subject", subject, "body", body)
	return nil
}
