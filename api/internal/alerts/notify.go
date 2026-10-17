package alerts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"bm-monitor/api/internal/config"
)

// Message is what every channel sends.
type Message struct {
	Subject string
	Body    string
}

// Senders delivers messages. Secrets come from config (env), addresses from the channel row.
type Senders struct {
	cfg  *config.Config
	http *http.Client
}

func NewSenders(cfg *config.Config) *Senders {
	return &Senders{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second}}
}

// ChannelConfig is the non-secret part of a channel, stored as jsonb.
type ChannelConfig struct {
	To     []string `json:"to,omitempty"`      // email recipients
	ChatID string   `json:"chat_id,omitempty"` // telegram override
}

// Available reports whether the env provides the secrets a channel kind needs.
func (s *Senders) Available(kind string) bool {
	switch kind {
	case "email":
		return s.cfg.SMTP.Enabled()
	case "slack":
		return s.cfg.SlackWebhookURL != ""
	case "telegram":
		return s.cfg.TelegramBotToken != ""
	case "discord":
		return s.cfg.DiscordWebhookURL != ""
	}
	return false
}

func (s *Senders) Send(ctx context.Context, kind string, cc ChannelConfig, m Message) error {
	switch kind {
	case "email":
		return s.sendEmail(ctx, cc.To, m)
	case "slack":
		return s.postJSON(ctx, s.cfg.SlackWebhookURL, map[string]string{"text": "*" + m.Subject + "*\n" + m.Body})
	case "discord":
		return s.postJSON(ctx, s.cfg.DiscordWebhookURL, map[string]string{"content": "**" + m.Subject + "**\n" + truncate(m.Body, 1800)})
	case "telegram":
		chat := cc.ChatID
		if chat == "" {
			chat = s.cfg.TelegramChatID
		}
		if s.cfg.TelegramBotToken == "" || chat == "" {
			return errors.New("telegram is not configured (TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID)")
		}
		return s.postJSON(ctx, "https://api.telegram.org/bot"+s.cfg.TelegramBotToken+"/sendMessage",
			map[string]string{"chat_id": chat, "text": m.Subject + "\n\n" + m.Body})
	}
	return fmt.Errorf("unknown channel kind %q", kind)
}

func (s *Senders) postJSON(ctx context.Context, url string, payload any) error {
	if url == "" {
		return errors.New("webhook URL is not configured")
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return errors.New("webhook request failed") // the URL holds a secret: never echo it
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook answered HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *Senders) sendEmail(ctx context.Context, to []string, m Message) error {
	c := s.cfg.SMTP
	if !c.Enabled() {
		return errors.New("email is not configured (SMTP_HOST)")
	}
	if len(to) == 0 {
		return errors.New("no recipients")
	}
	for _, addr := range to {
		if _, err := mail.ParseAddress(addr); err != nil {
			return fmt.Errorf("invalid recipient %q", addr)
		}
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second, Deadline: deadline}
	tlsCfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	var err error
	if c.Port == 465 { // implicit TLS
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp hello: %w", err)
	}
	defer client.Close()
	if c.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("smtp server does not offer STARTTLS; refusing to send credentials in clear text")
		}
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if c.User != "" {
		if err := client.Auth(smtp.PlainAuth("", c.User, c.Password, c.Host)); err != nil {
			return errors.New("smtp authentication failed (check SMTP_USER / SMTP_PASSWORD)")
		}
	}
	if err := client.Mail(c.From); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, r := range to {
		if err := client.Rcpt(r); err != nil {
			return fmt.Errorf("smtp RCPT TO %s: %w", r, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	idb := make([]byte, 12)
	_, _ = rand.Read(idb)
	domain := c.From[strings.LastIndex(c.From, "@")+1:]
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: BM Monitor <%s>\r\n", c.From)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&buf, "Message-ID: <%s@%s>\r\n", hex.EncodeToString(idb), domain)
	buf.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	buf.WriteString(strings.ReplaceAll(m.Body, "\n", "\r\n"))
	buf.WriteString("\r\n")
	if _, err := w.Write(buf.Bytes()); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	return client.Quit()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// Notifier sends queued notifications with retries; it never evaluates rules.
type Notifier struct {
	pool      *pgxpool.Pool
	senders   *Senders
	log       *slog.Logger
	publicURL string
}

func NewNotifier(pool *pgxpool.Pool, senders *Senders, publicURL string, log *slog.Logger) *Notifier {
	return &Notifier{pool: pool, senders: senders, publicURL: strings.TrimRight(publicURL, "/"), log: log}
}

const maxAttempts = 5

func (n *Notifier) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := n.flush(ctx); err != nil && ctx.Err() == nil {
			n.log.Error("notifier failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type pending struct {
	id                         int64
	event                      string
	attempts                   int
	kind                       string
	cfg                        ChannelConfig
	ruleName, severity, msg    string
	state                      string
	serverName                 *string
	serverID                   *string
	startedAt                  time.Time
	resolvedAt, acknowledgedAt *time.Time
}

// flush claims due notifications (SKIP LOCKED, so several API instances never double-send) and sends them.
func (n *Notifier) flush(ctx context.Context) error {
	rows, err := n.pool.Query(ctx, `
		WITH due AS (
		  SELECT id FROM notifications WHERE status = 'queued' AND next_try_at <= now()
		  ORDER BY id LIMIT 20 FOR UPDATE SKIP LOCKED)
		UPDATE notifications nt SET next_try_at = now() + interval '2 minutes'
		FROM due, alerts a, alert_rules r, notification_channels c
		WHERE nt.id = due.id AND a.id = nt.alert_id AND r.id = a.rule_id AND c.id = nt.channel_id
		RETURNING nt.id, nt.event, nt.attempts, c.kind, c.config, r.name, a.severity, a.message, a.state,
		  (SELECT name FROM servers WHERE id = a.server_id), a.server_id::text, a.started_at, a.resolved_at, a.acknowledged_at`)
	if err != nil {
		return err
	}
	var batch []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.event, &p.attempts, &p.kind, &p.cfg, &p.ruleName, &p.severity, &p.msg, &p.state,
			&p.serverName, &p.serverID, &p.startedAt, &p.resolvedAt, &p.acknowledgedAt); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, p)
	}
	rows.Close()
	for _, p := range batch {
		sctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := n.senders.Send(sctx, p.kind, p.cfg, n.render(p))
		cancel()
		if err == nil {
			_, err = n.pool.Exec(ctx, `UPDATE notifications SET status = 'sent', sent_at = now(), attempts = attempts + 1, last_error = NULL WHERE id = $1`, p.id)
			if err != nil {
				return err
			}
			continue
		}
		attempts := p.attempts + 1
		status := "queued"
		if attempts >= maxAttempts {
			status = "failed"
		}
		backoff := time.Duration(30*(1<<(attempts-1))) * time.Second
		n.log.Warn("notification failed", "id", p.id, "channel", p.kind, "attempt", attempts, "err", err)
		if _, err := n.pool.Exec(ctx, `UPDATE notifications SET attempts = $2, status = $3, last_error = $4, next_try_at = now() + $5::interval WHERE id = $1`,
			p.id, attempts, status, truncate(err.Error(), 500), fmt.Sprintf("%d seconds", int(backoff.Seconds()))); err != nil {
			return err
		}
	}
	return nil
}

func (n *Notifier) render(p pending) Message {
	label := map[string]string{"fired": "FIRING", "reminder": "STILL FIRING", "resolved": "RESOLVED"}[p.event]
	subject := fmt.Sprintf("[%s] %s: %s", label, strings.ToUpper(p.severity), p.ruleName)
	if p.serverName != nil {
		subject += " on " + *p.serverName
	}
	var b strings.Builder
	b.WriteString(p.msg + "\n\n")
	fmt.Fprintf(&b, "Rule:     %s\nSeverity: %s\nStarted:  %s UTC\n", p.ruleName, p.severity, p.startedAt.UTC().Format("2006-01-02 15:04:05"))
	if p.resolvedAt != nil {
		fmt.Fprintf(&b, "Resolved: %s UTC (lasted %s)\n", p.resolvedAt.UTC().Format("2006-01-02 15:04:05"), p.resolvedAt.Sub(p.startedAt).Round(time.Second))
	}
	if p.event == "reminder" {
		b.WriteString("\nThis alert is still firing and has not been acknowledged.\n")
	}
	if n.publicURL != "" {
		if p.serverID != nil {
			fmt.Fprintf(&b, "\nServer: %s/servers/%s\n", n.publicURL, *p.serverID)
		}
		fmt.Fprintf(&b, "Alerts: %s/alerts\n", n.publicURL)
	}
	b.WriteString("\n-- BM Monitor")
	return Message{Subject: subject, Body: b.String()}
}
