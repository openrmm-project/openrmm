package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// Owning the X11 CLIPBOARD selection means answering SelectionRequest events for
// as long as the content is meant to survive, which is a service this process is
// not shaped to run — the capture loop owns the X connection and nothing else
// reads its event queue. The desktop's own clipboard tools already run that
// service, so the text is handed to whichever is installed. Wayland first: on a
// Wayland session the X11 tools reach Xwayland's clipboard, which is not the one
// the compositor's applications read.
var clipboardTools = [][]string{
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
}

// Reading uses the same tools in the same order, for the same reason.
var clipboardReadTools = [][]string{
	{"wl-paste", "--no-newline"},
	{"xclip", "-selection", "clipboard", "-o"},
	{"xsel", "--clipboard", "--output"},
}

func setClipboard(text string) error {
	tried := []string{}
	for _, tool := range clipboardTools {
		path, err := exec.LookPath(tool[0])
		if err != nil {
			continue
		}
		tried = append(tried, tool[0])
		cmd := exec.Command(path, tool[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	if len(tried) == 0 {
		return fmt.Errorf("no clipboard tool found (install wl-clipboard, xclip or xsel)")
	}
	return fmt.Errorf("clipboard tools failed: %s", strings.Join(tried, ", "))
}

// readClipboard returns what the endpoint currently holds. An empty clipboard
// and a missing tool are different answers: the first is normal, the second is
// reported so the operator learns why mirroring never works on this host.
func readClipboard() (string, error) {
	tried := []string{}
	for _, tool := range clipboardReadTools {
		path, err := exec.LookPath(tool[0])
		if err != nil {
			continue
		}
		tried = append(tried, tool[0])
		out, err := exec.Command(path, tool[1:]...).Output()
		if err == nil {
			return string(out), nil
		}
	}
	if len(tried) == 0 {
		return "", fmt.Errorf("no clipboard tool found (install wl-clipboard, xclip or xsel)")
	}
	return "", fmt.Errorf("clipboard tools failed: %s", strings.Join(tried, ", "))
}
