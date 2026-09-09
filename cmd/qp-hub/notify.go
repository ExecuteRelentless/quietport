package main

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
	"strings"
)

// notifyOperator (FR-104): an event is always recorded; an email goes out when QP_SMTP_* is configured.
// QP_SMTP_HOST=smtp.example.com:587 QP_SMTP_USER=... QP_SMTP_PASS=... QP_SMTP_FROM=hub@quietport.app QP_NOTIFY_TO=you@example.com
func (h *Hub) notifyOperator(subject, body string) {
	host, user, pass, from, to := os.Getenv("QP_SMTP_HOST"), os.Getenv("QP_SMTP_USER"), os.Getenv("QP_SMTP_PASS"), os.Getenv("QP_SMTP_FROM"), os.Getenv("QP_NOTIFY_TO")
	if host == "" || to == "" {
		return
	}
	if from == "" {
		from = user
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s\r\n", from, to, subject, body)
	var auth smtp.Auth
	if user != "" {
		auth = smtp.PlainAuth("", user, pass, strings.Split(host, ":")[0])
	}
	if err := smtp.SendMail(host, auth, from, []string{to}, []byte(msg)); err != nil {
		log.Printf("notify: %v", err)
	}
}
