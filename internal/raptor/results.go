package raptor

import (
	"encoding/json"
	"fmt"
)

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func Truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + fmt.Sprintf("\n\n[... truncated %d bytes ...]", len(s)-maxBytes)
}

func FormatJSON(rows []map[string]any) (string, error) {
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
