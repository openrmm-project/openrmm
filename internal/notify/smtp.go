// Package notify delivers outbound email for alerts and password resets.
//
// It deliberately uses only net/smtp from the standard library: this server runs
// with SYSTEM-level reach over the whole fleet, so every dependency added here is
// a dependency in that blast radius.
package notify

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"syscall"
	"time"
)

// Config is the SMTP transport configuration, sourced from app settings so an
// operator can fix a wrong password without a redeploy.
type Config struct {
	Host       string
	Port       int
	Username   string
	Password   string
	From       string
	FromName   string
	TLSMode    string // starttls | tls | none
	SkipVerify bool
	Timeout    time.Duration
	// PublicOnly refuses to connect to anything but a public address. Set on
	// relays a console user typed in; enforced on the address actually dialled,
	// so a name that resolves somewhere else later (DNS rebinding) is caught too.
	PublicOnly bool
	// Operator marks the operator's shared alert service (RMM_ALERTS_SMTP_*):
	// its sender is the operator's, so it delivers only to confirmed
	// addresses, at most HourlyLimit messages an hour, with a footer naming
	// the instance the alert came from.
	Operator    bool
	HourlyLimit int
}

// ErrNotPublic is returned when PublicOnly refuses an address.
var ErrNotPublic = errors.New("not a public address")

// PublicIP reports whether an address is on the public internet: not loopback,
// private (RFC 1918, IPv6 ULA), link-local (cloud metadata lives there),
// carrier-grade NAT, unspecified or multicast.
func PublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0, // "this network"
			v4[0] == 100 && v4[1]&0xc0 == 64,         // 100.64.0.0/10, carrier-grade NAT
			v4[0] == 192 && v4[1] == 0 && v4[2] == 0, // 192.0.0.0/24, protocol assignments
			v4[0] >= 240:                             // reserved and broadcast
			return false
		}
	}
	return true
}

// refuseNonPublic is a net.Dialer Control hook: it runs after resolution, on
// the address about to be dialled.
func refuseNonPublic(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if !PublicIP(net.ParseIP(host)) {
		return fmt.Errorf("refusing to connect to %s: %w", host, ErrNotPublic)
	}
	return nil
}

// Enabled reports whether email can be sent at all.
func (c Config) Enabled() bool { return c.Host != "" && c.From != "" }

// Validate catches the misconfigurations that would otherwise surface as a
// stuck outbox hours later.
func (c Config) Validate() error {
	if c.Host == "" {
		return errors.New("SMTP host is required")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid SMTP port %d", c.Port)
	}
	if c.From == "" {
		return errors.New("sender address is required")
	}
	switch c.TLSMode {
	case "starttls", "tls", "none":
	default:
		return fmt.Errorf("invalid TLS mode %q (want starttls, tls or none)", c.TLSMode)
	}
	if c.TLSMode == "none" && c.Password != "" {
		// Refusing beats silently shipping the operator's mailbox password in
		// clear text across the network.
		return errors.New("refusing to send SMTP credentials over an unencrypted connection")
	}
	return nil
}

// Message is one email to deliver.
type Message struct {
	To      string
	Subject string
	Body    string // plain text
}

// Send delivers a single message. Callers are expected to run this off the
// request/scheduler path — an unreachable SMTP server takes the full timeout.
func Send(cfg Config, msg Message) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(msg.To) == "" {
		return errors.New("recipient is required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	tlsCfg := &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: cfg.SkipVerify} //nolint:gosec // opt-in for private CAs

	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: timeout}
	if cfg.PublicOnly {
		dialer.Control = refuseNonPublic
	}
	if cfg.TLSMode == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer client.Close()

	if cfg.TLSMode == "starttls" {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return errors.New("server does not offer STARTTLS (set TLS mode explicitly if this is intended)")
		}
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	if cfg.Username != "" {
		// PlainAuth refuses to run on an unencrypted link unless the host is
		// localhost, which is the behaviour we want; the Validate check above
		// makes the failure explicit rather than a surprise here.
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := client.Rcpt(msg.To); err != nil {
		return fmt.Errorf("RCPT TO %s: %w", msg.To, err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write([]byte(render(cfg, msg))); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return client.Quit()
}

// render builds the RFC 5322 message. Headers are sanitised because subjects
// carry hostnames and check names that ultimately come from an endpoint: a
// newline smuggled into one of them would let an attacker inject headers and
// redirect the mail.
func render(cfg Config, msg Message) string {
	from := cfg.From
	if cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", sanitizeHeader(cfg.FromName)), cfg.From)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", sanitizeHeader(msg.To))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", sanitizeHeader(msg.Subject)))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n") // keeps vacation responders quiet
	b.WriteString("\r\n")
	b.WriteString(normalizeBody(msg.Body))
	return b.String()
}

func sanitizeHeader(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

// normalizeBody applies CRLF line endings and dot-stuffing, so a body line that
// happens to be "." cannot terminate the DATA command early.
func normalizeBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, ".") {
			lines[i] = "." + line
		}
	}
	out := strings.Join(lines, "\r\n")
	if !strings.HasSuffix(out, "\r\n") {
		out += "\r\n"
	}
	return out
}
