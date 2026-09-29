package notify

import (
	"strings"
	"testing"
)

// The footer says which instance an alert came from and that its words are the
// instance's: the sender domain is the operator's.
func TestOperatorFooterNamesTheInstance(t *testing.T) {
	f := operatorFooter("https://acme.openrmm.io")
	if !strings.Contains(f, "acme.openrmm.io") || !strings.Contains(f, "comes from that instance") {
		t.Fatalf("footer = %q", f)
	}
	if strings.Contains(f, "https://") {
		t.Errorf("the footer should name the host, not a link: %q", f)
	}
}
