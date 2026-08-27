package smtp

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	netSMTP "net/smtp"
	"strings"
	"time"
)

const (
	dialTimeout  = 10 * time.Second
	sendDeadline = 30 * time.Second
)

type Config struct {
	Host        string
	Port        int
	Username    string
	Password    string
	FromAddress string
}

type Client struct {
	cfg Config
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg}
}

func sanitizeHeader(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

func (c *Client) SendEmail(to, subject, body, contentType string) error {
	addr := net.JoinHostPort(c.cfg.Host, fmt.Sprintf("%d", c.cfg.Port))

	// Dial with timeout so a blackholed SMTP host cannot hang the caller indefinitely.
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer conn.Close()

	// Set overall send deadline for the entire SMTP transaction.
	if err := conn.SetDeadline(time.Now().Add(sendDeadline)); err != nil {
		return fmt.Errorf("smtp set deadline: %w", err)
	}

	client, err := netSMTP.NewClient(conn, c.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp new client: %w", err)
	}
	defer client.Close()

	// Upgrade to TLS if the server supports STARTTLS (same behaviour as net/smtp.SendMail).
	if ok, _ := client.Extension("STARTTLS"); ok {
		config := &tls.Config{ServerName: c.cfg.Host}
		if err := client.StartTLS(config); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	// Authenticate if credentials are provided.
	if c.cfg.Username != "" {
		auth := netSMTP.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	// Set sender and recipient.
	if err := client.Mail(c.cfg.FromAddress); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}

	// Write message body.
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: %s; charset=\"UTF-8\"\r\n\r\n%s",
		sanitizeHeader(c.cfg.FromAddress), sanitizeHeader(to), sanitizeHeader(subject), contentType, body)

	if _, err := io.WriteString(w, msg); err != nil {
		w.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp write close: %w", err)
	}

	return client.Quit()
}

func (c *Client) SendTextEmail(to, subject, body string) error {
	return c.SendEmail(to, subject, body, "text/plain")
}

func (c *Client) SendHTMLEmail(to, subject, htmlBody string) error {
	return c.SendEmail(to, subject, htmlBody, "text/html")
}
