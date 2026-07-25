package smtp

import (
	"fmt"
	netSMTP "net/smtp"
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

func (c *Client) SendEmail(to, subject, body, contentType string) error {
	addr := fmt.Sprintf("%s:%d", c.cfg.Host, c.cfg.Port)
	auth := netSMTP.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)
	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: %s; charset=\"UTF-8\"\r\n\r\n%s",
		c.cfg.FromAddress, to, subject, contentType, body))
	return netSMTP.SendMail(addr, auth, c.cfg.FromAddress, []string{to}, msg)
}

func (c *Client) SendTextEmail(to, subject, body string) error {
	return c.SendEmail(to, subject, body, "text/plain")
}

func (c *Client) SendHTMLEmail(to, subject, htmlBody string) error {
	return c.SendEmail(to, subject, htmlBody, "text/html")
}
