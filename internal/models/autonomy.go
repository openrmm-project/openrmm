package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ScriptContentHash is what a release for the AI is bound to: the shell, the
// body and the declared parameters. Name, description and category are left
// out, so renaming a script keeps its release; anything that changes what runs,
// or what a caller may pass to it, does not.
func ScriptContentHash(shell, body string, params []ScriptParam) string {
	if params == nil {
		params = []ScriptParam{}
	}
	raw, _ := json.Marshal(struct {
		Shell  string        `json:"shell"`
		Body   string        `json:"body"`
		Params []ScriptParam `json:"params"`
	}{shell, body, params})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// AIReleaseCurrent reports whether a script's release still matches what it
// would run now.
func (sc *Script) AIReleaseCurrent() bool {
	return sc.AIReleasedHash != "" && sc.AIReleasedHash == ScriptContentHash(sc.Shell, sc.Body, sc.Params)
}
