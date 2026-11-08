package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// prepareInventory removes actual NUL characters from collected text before
// PostgreSQL JSONB rejects the entire snapshot. Do this on the server so agents
// already installed can report without an upgrade. Decode strings rather than
// replacing raw \\u0000 sequences, which would corrupt literal paths/text.
func prepareInventory(data []byte) ([]byte, error) {
	if !json.Valid(data) {
		return nil, errors.New("inventory must be valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // hardware sizes and other integers must keep their precision
	var snapshot map[string]any
	if err := dec.Decode(&snapshot); err != nil || snapshot == nil {
		return nil, errors.New("inventory must be a JSON object")
	}
	if err := cleanInventoryText(snapshot); err != nil {
		return nil, err
	}
	return json.Marshal(snapshot)
}

func cleanInventoryText(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			// Collected field names are fixed. Renaming arbitrary keys could
			// overwrite another field, so refuse those instead.
			if strings.ContainsRune(key, '\x00') {
				return errors.New("inventory field names must not contain NUL")
			}
			if text, ok := item.(string); ok {
				v[key] = strings.ReplaceAll(text, "\x00", "")
			} else if err := cleanInventoryText(item); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range v {
			if text, ok := item.(string); ok {
				v[i] = strings.ReplaceAll(text, "\x00", "")
			} else if err := cleanInventoryText(item); err != nil {
				return err
			}
		}
	}
	return nil
}
