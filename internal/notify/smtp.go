// Package notify delivers outbound email for alerts and password resets.
//
// It deliberately uses only the standard library: this server runs
// with SYSTEM-level reach over the whole fleet, so every dependency added here is
// a dependency in that blast radius.
package notify

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
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
	from, err := parseMailbox(cfg.From)
	if err != nil {
		return fmt.Errorf("sender: %w", err)
	}
	to, err := parseMailbox(msg.To)
	if err != nil {
		return fmt.Errorf("recipient: %w", err)
	}
	if cfg.FromName != "" {
		from.Name = sanitizeHeader(cfg.FromName)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	tlsCfg := &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: cfg.SkipVerify} //nolint:gosec // opt-in for private CAs

	var conn net.Conn
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
	if err := client.Mail(envelopeMailbox(from)); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := client.Rcpt(envelopeMailbox(to)); err != nil {
		return fmt.Errorf("RCPT TO %s: %w", to.Address, err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if err := writeMessage(w, from, to, msg); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return client.Quit()
}

func parseMailbox(raw string) (*mail.Address, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return nil, errors.New("invalid mailbox address")
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return nil, errors.New("invalid mailbox address")
	}
	addr.Name = sanitizeHeader(addr.Name)
	return addr, nil
}

func envelopeMailbox(addr *mail.Address) string {
	// Format without a display name, restoring any required local-part quoting.
	// net/smtp supplies the surrounding angle brackets.
	address := (&mail.Address{Address: addr.Address}).String()
	return address[1 : len(address)-1]
}

// writeMessage writes typed addresses and a MIME text body. The SMTP DATA
// writer handles dot-stuffing on the wire.
func writeMessage(w io.Writer, from, to *mail.Address, msg Message) error {
	const headers = "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\nAuto-Submitted: auto-generated\r\n\r\n"
	subject := mime.QEncoding.Encode("utf-8", sanitizeHeader(msg.Subject))
	if _, err := fmt.Fprintf(w, headers, from.String(), to.String(), subject, time.Now().Format(time.RFC1123Z)); err != nil {
		return err
	}
	body := msg.Body
	if !strings.HasSuffix(body, "\n") && !strings.HasSuffix(body, "\r") {
		body += "\n"
	}
	// The text-mode encoder canonicalizes LF, CRLF and bare CR to CRLF.
	encoder := quotedprintable.NewWriter(w)
	if _, err := io.WriteString(encoder, body); err != nil {
		return err
	}
	return encoder.Close()
}

func sanitizeHeader(v string) string {
	return strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(v)
}
