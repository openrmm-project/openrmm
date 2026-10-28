package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/protocol"
)

func TestParseAptUpgradeReadsInstLinesAndSecurityOrigin(t *testing.T) {
	out := `NOTE: This is only a simulation!
Reading package lists...
Inst openssl [3.0.2-0ubuntu1.10] (3.0.2-0ubuntu1.12 Ubuntu:22.04/jammy-security [amd64])
Inst curl [7.81.0-1ubuntu1.13] (7.81.0-1ubuntu1.14 Ubuntu:22.04/jammy-updates [amd64])
Conf openssl (3.0.2-0ubuntu1.12 Ubuntu:22.04/jammy-security [amd64])
Inst
`
	items := parseAptUpgrade(out)
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Identifier != "openssl" || items[0].Version != "3.0.2-0ubuntu1.12" || items[0].Classification != protocol.PatchSecurity {
		t.Fatalf("openssl = %+v", items[0])
	}
	if items[1].Identifier != "curl" || items[1].Classification != protocol.PatchOther || items[1].Title != "curl 7.81.0-1ubuntu1.14" {
		t.Fatalf("curl = %+v", items[1])
	}
	if got := parseAptUpgrade(""); len(got) != 0 {
		t.Fatalf("nothing to upgrade: %+v", got)
	}
}

func TestParseDnfUpdateinfoClassifiesAdvisories(t *testing.T) {
	out := `RHSA-2026:1234 Important/Sec. openssl-1:3.0.7-27.el9.x86_64
RHBA-2026:0042 bugfix       tzdata-2026a-1.el9.noarch
too short
`
	items := parseDnfUpdateinfo(out)
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Identifier != "openssl-1:3.0.7-27.el9.x86_64" || items[0].Classification != protocol.PatchImportant ||
		items[0].Title != "openssl-1:3.0.7-27.el9.x86_64 (RHSA-2026:1234)" {
		t.Fatalf("openssl = %+v", items[0])
	}
	if items[1].Classification != protocol.PatchOptional {
		t.Fatalf("bugfix = %+v", items[1])
	}
}

func TestClassifyDnf(t *testing.T) {
	for typ, want := range map[string]string{
		"Critical/Sec.":  protocol.PatchCritical,
		"Important/Sec.": protocol.PatchImportant,
		"Moderate/Sec.":  protocol.PatchModerate,
		"Low/Sec.":       protocol.PatchSecurity,
		"bugfix":         protocol.PatchOptional,
		"enhancement":    protocol.PatchOther,
	} {
		if got := classifyDnf(typ); got != want {
			t.Errorf("classifyDnf(%q) = %q, want %q", typ, got, want)
		}
	}
}

func TestParseSoftwareUpdateListReadsLabelsAndRestarts(t *testing.T) {
	out := `Software Update Tool

Finding available software
Software Update found the following new or updated software:
* Label: macOS Sonoma 14.7.1-23H222
	Title: macOS Sonoma 14.7.1, Version: 14.7.1, Size: 1612000KiB, Recommended: YES, Action: restart,
* Label: Safari18.1SonomaAuto-18.1
	Title: Safari, Version: 18.1, Size: 160000KiB, Recommended: YES,
* Label: Security Response 1
	Title: Background Security Improvement, Version: 1, Size: 10KiB,`
	items := parseSoftwareUpdateList(out)
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Identifier != "macOS Sonoma 14.7.1-23H222" || !items[0].RebootRequired {
		t.Fatalf("macOS = %+v", items[0])
	}
	if items[1].RebootRequired || items[1].Classification != protocol.PatchOther {
		t.Fatalf("Safari = %+v", items[1])
	}
	if items[2].Classification != protocol.PatchSecurity {
		t.Fatalf("security title = %+v", items[2])
	}
}

// The identifiers reach apt-get, dnf and softwareupdate as arguments, and a
// PowerShell script on Windows: none may become an option or a fragment.
func TestValidPatchIdentifier(t *testing.T) {
	cases := []struct {
		goos, id string
		want     bool
	}{
		{"windows", "KB5034441", true},
		{"windows", "0a1b2c3d-4e5f-6789-abcd-ef0123456789", true},
		{"windows", "KB123'; Remove-Item C:\\ -Recurse; '", false},
		{"windows", "", false},
		{"linux", "openssl", true},
		{"linux", "libstdc++6", true},
		{"linux", "openssl-1:3.0.7-27.el9.x86_64", true},
		{"linux", "-oAPT::Update::Pre-Invoke::=touch /tmp/x", false},
		{"linux", "pkg name", false},
		{"linux", "", false},
		{"darwin", "macOS Sonoma 14.7.1-23H222", true},
		{"darwin", "-a", false},
		{"darwin", "label\nwith newline", false},
		{"darwin", strings.Repeat("x", 257), false},
	}
	for _, c := range cases {
		if got := validPatchIdentifier(c.goos, c.id); got != c.want {
			t.Errorf("validPatchIdentifier(%s, %q) = %v, want %v", c.goos, c.id, got, c.want)
		}
	}
}

func TestSplitPatchIdentifiersRefusesMalformedOnes(t *testing.T) {
	ids, refused := splitPatchIdentifiers("linux", []string{"curl", "--allow-downgrades", "openssl"})
	if strings.Join(ids, ",") != "curl,openssl" {
		t.Fatalf("ids = %v", ids)
	}
	if len(refused) != 1 || refused[0].Identifier != "--allow-downgrades" || refused[0].Success || refused[0].Error == "" {
		t.Fatalf("refused = %+v", refused)
	}
}

// With nothing well-formed left, no package manager runs at all: an empty list
// would mean "install everything" to apt-get and softwareupdate.
func TestInstallPatchesNeverRunsWithOnlyRefusedIdentifiers(t *testing.T) {
	results, reboot := installPatches([]string{"-a", "--help", "x\ny"})
	if reboot || len(results) != 3 {
		t.Fatalf("results = %+v, reboot %v", results, reboot)
	}
	for _, r := range results {
		if r.Success || !strings.Contains(r.Error, "refused") {
			t.Fatalf("result = %+v", r)
		}
	}
	if res, reboot := installPatches(nil); res != nil || reboot {
		t.Fatalf("empty install = %+v %v", res, reboot)
	}
}

func TestUniformResultAndErrOut(t *testing.T) {
	res, reboot := uniformResult([]string{"a", "b"}, true, true, "")
	if len(res) != 2 || !res[0].Success || !res[1].RebootRequired || !reboot {
		t.Fatalf("success = %+v %v", res, reboot)
	}
	res, reboot = uniformResult([]string{"a"}, false, true, "boom")
	if res[0].Success || res[0].Error != "boom" || reboot {
		t.Fatalf("failure = %+v %v", res, reboot)
	}
	if errOut(nil, []byte("ignored")) != "" {
		t.Fatal("no error, no message")
	}
	if got := errOut(errors.New("exit 1"), nil); got != "exit 1" {
		t.Fatalf("empty output = %q", got)
	}
	long := strings.Repeat("a", 900) + "THE END"
	if got := errOut(errors.New("x"), []byte(long)); len(got) != 800 || !strings.HasSuffix(got, "THE END") {
		t.Fatalf("long output keeps its last 800 bytes: %d", len(got))
	}
}

func TestHandlePatchInstallWithoutIdentifiersSendsNothing(t *testing.T) {
	a := &agent{send: make(chan []byte, 1)}
	a.handlePatchInstall(protocol.PatchInstallMsg{RequestID: "r1"})
	select {
	case frame := <-a.send:
		t.Fatalf("queued %s", frame)
	default:
	}
}
