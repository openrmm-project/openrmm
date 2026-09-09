package api

import (
	"testing"

	"github.com/p1ngu/rmm/internal/config"
)

// safeRedirectPath is the one place a caller-supplied destination reaches a
// redirect. An open redirect here is not a cosmetic bug: it makes a phishing
// link that starts on the real console domain, survives a careful look at the
// address bar, and lands on someone else's login form.
func TestSafeRedirectPathKeepsTheBrowserOnThisSite(t *testing.T) {
	kept := []struct{ in, want string }{
		{"/devices", "/devices"},
		{"/devices?tab=alerts", "/devices?tab=alerts"},
		{"/", "/"},
	}
	for _, c := range kept {
		if got := safeRedirectPath(c.in); got != c.want {
			t.Errorf("safeRedirectPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Each of these is a way to leave the site. They are discarded, not
	// repaired: a "corrected" hostile redirect is still a redirect someone
	// crafted, and the cost of dropping it is one extra click for a legitimate
	// caller who sent something malformed.
	rejected := []string{
		"https://evil.example/steal",  // absolute
		"http://evil.example",         // absolute, plain
		"//evil.example",              // protocol-relative: a browser treats this as a host
		"///evil.example",             // three slashes, same trick
		"\\\\evil.example",            // backslashes, which some parsers fold to slashes
		"/\\evil.example",             // mixed, same reason
		"javascript:alert(1)",         // not a path at all
		"devices",                     // relative, ambiguous against the base
		"",                            // nothing asked for
		"/devices\r\nSet-Cookie: a=b", // header injection through the Location value
		"/devices\nX: y",
	}
	for _, in := range rejected {
		if got := safeRedirectPath(in); got != "" {
			t.Errorf("safeRedirectPath(%q) = %q, want it discarded", in, got)
		}
	}
}

// consoleURL builds the address the browser is sent back to. It must never be
// able to point off-site, whatever it is handed.
func TestConsoleURLStaysOnTheConsole(t *testing.T) {
	s := &Server{cfg: &config.Config{PublicURL: "https://rmm.example.com/"}}

	if got := s.consoleURL(""); got != "https://rmm.example.com/" {
		t.Errorf("consoleURL(\"\") = %q", got)
	}
	if got := s.consoleURL("/devices"); got != "https://rmm.example.com/devices" {
		t.Errorf("consoleURL(\"/devices\") = %q", got)
	}
	// Whatever safeRedirectPath already refused cannot arrive here, but the
	// concatenation is checked anyway: "//evil.example" appended to a bare base
	// is a protocol-relative URL.
	if got := s.consoleURL(safeRedirectPath("//evil.example")); got != "https://rmm.example.com/" {
		t.Errorf("consoleURL of a rejected path = %q, want the console root", got)
	}
}
