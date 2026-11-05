// Package autonomy holds the server-side rules for what the AI may do on a
// device without a person approving it at the time (docs/23-ai-autonomy.md):
// which library scripts can be released, what their parameters may be, and
// what a caller's values must look like.
//
// The rules are enforced here, whatever an administrator marks. They decide
// what can be released; who released it, and whether a role may use releases,
// are checked by the callers.
package autonomy

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/p1ngu/rmm/internal/models"
)

// Limits on what a release can carry.
const (
	MaxParams      = 8
	MaxEnumValues  = 50
	MaxValueLength = 200
	MaxPatternLen  = 200
)

var paramName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// safeValue is every character a parameter value may hold, whatever its list
// or pattern accepts. Environment variables keep a value from becoming code in
// the script itself, but a script could still hand it to something that parses
// it again (a nested shell, a command line built from strings). Without quotes,
// semicolons, ampersands, pipes, redirections, dollars, backticks, brackets,
// percent or exclamation signs, a value is inert there too.
var safeValue = regexp.MustCompile(`^[A-Za-z0-9 ._:@/\\+=,#-]{1,200}$`)

const safeValueChars = "letters, digits, spaces and . _ : @ / \\ + = , # -"

// EnvName is the environment variable a parameter reaches the script as.
func EnvName(param string) string {
	return "OPENRMM_PARAM_" + strings.ToUpper(param)
}

// ValidateParamSchema checks the parameters a script declares.
//
// cmd scripts may declare none: cmd expands %VAR% before it parses the line, so
// a value would become syntax there however it was passed. Every other shell
// reads an environment variable as data.
func ValidateParamSchema(shell string, params []models.ScriptParam) error {
	if len(params) == 0 {
		return nil
	}
	if shell == models.ShellCmd {
		return fmt.Errorf("cmd scripts cannot take parameters: cmd expands variables into the command line; use powershell")
	}
	if len(params) > MaxParams {
		return fmt.Errorf("at most %d parameters", MaxParams)
	}
	seen := map[string]bool{}
	for _, p := range params {
		if !paramName.MatchString(p.Name) {
			return fmt.Errorf("parameter name %q: use lowercase letters, digits and _ (up to 32, starting with a letter)", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("parameter %q is declared twice", p.Name)
		}
		seen[p.Name] = true
		switch p.Kind {
		case "enum":
			if len(p.Values) == 0 || len(p.Values) > MaxEnumValues {
				return fmt.Errorf("parameter %q: list 1 to %d accepted values", p.Name, MaxEnumValues)
			}
			for _, v := range p.Values {
				if err := checkValueText(v); err != nil {
					return fmt.Errorf("parameter %q value %q: %w", p.Name, v, err)
				}
			}
			if p.Pattern != "" {
				return fmt.Errorf("parameter %q: an enum parameter takes no pattern", p.Name)
			}
		case "pattern":
			if p.Pattern == "" || len(p.Pattern) > MaxPatternLen {
				return fmt.Errorf("parameter %q: give a pattern of up to %d characters", p.Name, MaxPatternLen)
			}
			if _, err := compilePattern(p.Pattern); err != nil {
				return fmt.Errorf("parameter %q: invalid pattern: %w", p.Name, err)
			}
			if len(p.Values) > 0 {
				return fmt.Errorf("parameter %q: a pattern parameter takes no value list", p.Name)
			}
		default:
			return fmt.Errorf("parameter %q: kind must be enum or pattern", p.Name)
		}
	}
	return nil
}

// compilePattern anchors a declared pattern on both ends, so "[a-z]+" means
// "only letters" rather than "contains a letter".
func compilePattern(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(`^(?:` + pattern + `)$`)
}

func checkValueText(v string) error {
	if v == "" {
		return fmt.Errorf("empty")
	}
	if len(v) > MaxValueLength {
		return fmt.Errorf("longer than %d characters", MaxValueLength)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return fmt.Errorf("contains a control character")
		}
	}
	if !safeValue.MatchString(v) {
		return fmt.Errorf("may hold only %s", safeValueChars)
	}
	return nil
}

// ValidateParamValues checks a caller's values against the declared schema:
// every declared parameter given, nothing else, and each value accepted by its
// list or pattern. Numbers and booleans are accepted as their text.
func ValidateParamValues(schema []models.ScriptParam, given map[string]any) (map[string]string, error) {
	declared := map[string]models.ScriptParam{}
	for _, p := range schema {
		declared[p.Name] = p
	}
	for name := range given {
		if _, ok := declared[name]; !ok {
			return nil, fmt.Errorf("the script declares no parameter %q", name)
		}
	}
	out := map[string]string{}
	for _, p := range schema {
		raw, ok := given[p.Name]
		if !ok {
			return nil, fmt.Errorf("parameter %q is required", p.Name)
		}
		v, err := valueText(raw)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", p.Name, err)
		}
		if err := checkValueText(v); err != nil {
			return nil, fmt.Errorf("parameter %q: %w", p.Name, err)
		}
		switch p.Kind {
		case "enum":
			found := false
			for _, allowed := range p.Values {
				if v == allowed {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("parameter %q must be one of: %s", p.Name, strings.Join(p.Values, ", "))
			}
		case "pattern":
			re, err := compilePattern(p.Pattern)
			if err != nil || !re.MatchString(v) {
				return nil, fmt.Errorf("parameter %q does not match the accepted pattern", p.Name)
			}
		default:
			return nil, fmt.Errorf("parameter %q has no accepted values", p.Name)
		}
		out[p.Name] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func valueText(raw any) (string, error) {
	switch v := raw.(type) {
	case string:
		return v, nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	case nil:
		return "", fmt.Errorf("no value")
	default:
		return "", fmt.Errorf("must be text, a number or true/false")
	}
}

// A denied category and the patterns that place a script in it. Matching is on
// the script's text, case-insensitively, and errs towards refusing: a script
// refused here still runs the ordinary way, with a person approving it.
type denyRule struct {
	category string
	pattern  *regexp.Regexp
}

func rule(category string, patterns ...string) []denyRule {
	out := make([]denyRule, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, denyRule{category, regexp.MustCompile(`(?i)` + p)})
	}
	return out
}

var denyRules = func() []denyRule {
	var all []denyRule
	all = append(all, rule("reboot or shutdown",
		`\b(shutdown|reboot|poweroff|halt)\b`, `\b(restart|stop)-computer\b`, `\binit\s+[06]\b`,
		`\bsystemctl\s+(reboot|poweroff|halt|kexec|suspend|hibernate)\b`)...)
	all = append(all, rule("install or uninstall",
		`\b(winget|choco|chocolatey|scoop|msiexec)\b`,
		`\b(apt|apt-get|aptitude|yum|dnf|zypper|pacman|apk|brew|snap|flatpak|port)\s+(-\S+\s+)*(install|remove|purge|uninstall|erase|reinstall|upgrade|update|dist-upgrade|full-upgrade|autoremove)\b`,
		`\brpm\s+-\S*[iUeF]`, `\bdpkg\s+(-\S*[irP]|--(install|remove|purge|unpack))`,
		`\b(install|uninstall|update|save)-(package|module|script|windowsfeature|windowscapability)\b`,
		`\b(add|remove)-(appxpackage|appxprovisionedpackage|windowscapability|windowsfeature|windowspackage)\b`,
		`\b(enable|disable)-windowsoptionalfeature\b`, `\bdism(\.exe)?\b`,
		`\bpip[0-9.]*\s+(install|uninstall)\b`, `\bnpm\s+(i|install|uninstall|rm|remove)\b`, `\bgem\s+(install|uninstall)\b`,
		`\binstaller\s+-pkg\b`, `\bsoftwareupdate\b`)...)
	all = append(all, rule("deleting files",
		`\b(remove-item|clear-content|clear-recyclebin|format-volume|clear-disk|initialize-disk|remove-partition)\b`,
		`\b(del|erase|rd|rmdir|rm|unlink|shred|srm|sdelete|wipefs|truncate)\b`, `\bri\s`,
		`\bformat\s+[a-z]:`, `\b(diskpart|mkfs(\.\w+)?|fdisk|parted)\b`, `\bdd\s`, `\bcipher\s+/w`,
		`\.(delete|unlink|rmtree|remove)\s*\(`, `\bos\.(remove|unlink|rmdir|removedirs)\b`, `\bshutil\.rmtree\b`)...)
	all = append(all, rule("users and passwords",
		`\bnet(\.exe)?\s+(user|localgroup|group|accounts)\b`,
		`\b(new|set|remove|enable|disable|rename)-localuser\b`, `\b(add|remove)-localgroupmember\b`,
		`\b(new|set|remove)-aduser\b`, `\bset-adaccountpassword\b`,
		`\b(useradd|userdel|usermod|adduser|deluser|passwd|chpasswd|chage|gpasswd|groupadd|groupdel|groupmod|visudo|dscl|sysadminctl|pwpolicy)\b`)...)
	all = append(all, rule("firewall or network changes",
		`\bnetsh\b`, `\b(new|set|remove|enable|disable|copy|rename)-netfirewall\w*`,
		`\b(new|set|remove)-net(ipaddress|route|nat|ipinterface|neighbor|lbfo\w*)\b`,
		`\b(enable|disable|rename|restart|set)-netadapter\w*`, `\bset-dnsclient(serveraddress)?\b`,
		`\b(iptables|ip6tables|nft|ufw|firewall-cmd|pfctl|ipfw|nmcli|networksetup|scutil|ifconfig|route)\b`,
		`\bip\s+(-\S+\s+)*(addr|address|route|link|rule|neigh)\s+(add|del|delete|change|replace|set|flush)\b`,
		`(drivers\\etc\\hosts|/etc/hosts|/etc/resolv\.conf)`)...)
	all = append(all, rule("removing or stopping the agent",
		// A prefix match: the service is OpenRMMAgent, the binaries rmm-agent*.
		`\b(openrmm|rmm-agent|rmm-remote|rmmagent)`, `\buninstall\.(ps1|sh)\b`,
		`\bsc(\.exe)?\s+(delete|stop|config|failure)\b`)...)
	all = append(all, rule("patches",
		`\bwusa\b`, `\b(install|get|hide)-windowsupdate\b`, `\b(usoclient|wuauclt)\b`, `\bpswindowsupdate\b`)...)
	all = append(all, rule("scheduled tasks and persistence",
		`\bschtasks\b`, `\b(register|new|set|unregister|enable|disable)-scheduledtask\b`,
		`\bcrontab\b`, `/etc/cron`, `\bat\s+\d`, `\blaunchctl\s+(load|unload|bootstrap|bootout|submit|enable|disable)\b`,
		`\bsystemctl\s+(enable|disable|mask|unmask|edit|set-property)\b`, `\bnew-service\b`)...)
	all = append(all, rule("registry or security settings",
		`\breg(\.exe)?\s+(add|delete|import|restore|load|unload|copy)\b`,
		`\b(set|new|remove|rename|clear)-itemproperty\b`,
		`\b(set|add|remove)-mppreference\b`, `\bbcdedit\b`, `\bvssadmin\b`, `\bauditpol\b`,
		`\bwevtutil\s+(cl|clear-log)\b`, `\bclear-eventlog\b`, `\b(setenforce|csrutil|spctl)\b`,
		`\bset-executionpolicy\b`, `\bmanage-bde\b`)...)
	all = append(all, rule("parsing a command line again",
		`\bcmd(\.exe)?\s+(/\S+\s+)*/[ck]\b`, `\b(sh|bash|zsh|dash|ksh)\s+(-\S+\s+)*-c\b`,
		`\b(powershell|pwsh)(\.exe)?\s+(-\S+\s+)*-c(ommand)?\b`, `\bos\.(system|popen)\s*\(`, `shell\s*=\s*true`,
		`\binvoke-(command|item)\b`, `\[scriptblock\]::create`, `\badd-type\b`, `&\s*[($]`, `(^|[\s;(])\.\s*[($]`)...)
	all = append(all, rule("downloading or running code built at run time",
		`\b(invoke-expression|iex)\b`, `-enc(odedcommand)?\b`, `frombase64string`, `\beval\b`, `\bexec\s*\(`,
		`\bbase64\s+(-d|--decode|-D)\b`, `\|\s*(sh|bash|zsh|python[0-9.]*|powershell|pwsh|iex)\b`,
		`\b(curl|wget|invoke-webrequest|iwr|invoke-restmethod|irm|start-bitstransfer|bitsadmin|certutil)\b`,
		`downloadstring|downloadfile`, `\b(start-process|new-object\s+-com)\b`, `\bsource\s+<\(`)...)
	return all
}()

// paramRef is how a script reads its parameters. It is removed before the
// deny rules run: the variable name contains "OPENRMM", which the rule about
// the agent would otherwise read as a reference to the agent itself.
var paramRef = regexp.MustCompile(`(?i)OPENRMM_PARAM_[A-Z0-9_]+`)

// DeniedCategories lists, sorted and without repeats, the denied categories a
// script falls in. A non-empty list means it can never be released.
func DeniedCategories(body string) []string {
	body = paramRef.ReplaceAllString(body, "PARAM")
	seen := map[string]bool{}
	for _, r := range denyRules {
		if !seen[r.category] && r.pattern.MatchString(body) {
			seen[r.category] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// CheckRelease is every rule a release must pass: a shell the agent runs, a
// valid parameter schema, and nothing on the deny list.
func CheckRelease(sc *models.Script) error {
	if !models.SupportedShell(sc.Shell) {
		return fmt.Errorf("unsupported shell %q", sc.Shell)
	}
	if strings.TrimSpace(sc.Body) == "" {
		return fmt.Errorf("the script is empty")
	}
	if err := ValidateParamSchema(sc.Shell, sc.Params); err != nil {
		return err
	}
	if denied := DeniedCategories(sc.Body); len(denied) > 0 {
		return fmt.Errorf("this script cannot be released for the AI: it falls under %s, which always needs a person's approval", strings.Join(denied, "; "))
	}
	return nil
}
