// Package mail sends transactional email over SMTP using only the
// standard library (net/smtp).
package mail

import (
	"bytes"
	"fmt"
	"net/smtp"
	"text/template"

	"github.com/dewlonsystems/platform-go/config"
	"github.com/dewlonsystems/platform-go/errors"
)

// Mailer sends email via a configured SMTP server.
type Mailer struct {
	host, port         string
	username, password string
	from               string
	auth               smtp.Auth
}

// New builds a Mailer from application config.
func New(cfg *config.Config) *Mailer {
	return &Mailer{
		host:     cfg.SMTPHost,
		port:     cfg.SMTPPort,
		username: cfg.SMTPUsername,
		password: cfg.SMTPPassword,
		from:     cfg.SMTPFrom,
		auth:     smtp.PlainAuth("", cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPHost),
	}
}

// Send sends a plain-text email to a single recipient. net/smtp.SendMail
// negotiates STARTTLS automatically when the server advertises it (true
// for essentially every modern provider on port 587).
func (m *Mailer) Send(to, subject, body string) error {
	addr := fmt.Sprintf("%s:%s", m.host, m.port)

	msg := new(bytes.Buffer)
	fmt.Fprintf(msg, "From: %s\r\n", m.from)
	fmt.Fprintf(msg, "To: %s\r\n", to)
	fmt.Fprintf(msg, "Subject: %s\r\n", subject)
	fmt.Fprintf(msg, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(msg, "Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	fmt.Fprintf(msg, "\r\n%s\r\n", body)

	if err := smtp.SendMail(addr, m.auth, m.from, []string{to}, msg.Bytes()); err != nil {
		return errors.NewInternal("failed to send email", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Transactional templates used by the auth flow
// -----------------------------------------------------------------------------

var welcomeTemplate = template.Must(template.New("welcome").Parse(
	"Hi {{.Name}},\n\n" +
		"Welcome aboard — your account has been created successfully.\n\n" +
		"If you didn't request this, you can safely ignore this email.\n",
))

var resetTemplate = template.Must(template.New("reset").Parse(
	"Hi,\n\n" +
		"We received a request to reset your password. Click the link below to choose a new one:\n\n" +
		"{{.ResetLink}}\n\n" +
		"This link expires in {{.ExpiresIn}}. If you didn't request this, you can safely ignore this email.\n",
))

// SendWelcomeEmail sends a welcome message after successful registration.
func (m *Mailer) SendWelcomeEmail(to, name string) error {
	body, err := render(welcomeTemplate, map[string]string{"Name": name})
	if err != nil {
		return err
	}
	return m.Send(to, "Welcome!", body)
}

// SendPasswordResetEmail sends a password-reset link. expiresIn is a
// human-readable duration string (e.g. "1 hour") for display purposes.
func (m *Mailer) SendPasswordResetEmail(to, resetLink, expiresIn string) error {
	body, err := render(resetTemplate, map[string]string{
		"ResetLink": resetLink,
		"ExpiresIn": expiresIn,
	})
	if err != nil {
		return err
	}
	return m.Send(to, "Reset your password", body)
}

func render(t *template.Template, data any) (string, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", errors.NewInternal("failed to render email template", err)
	}
	return buf.String(), nil
}
