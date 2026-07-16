package raptor

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	validParamName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	validFieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
	validArtifact  = regexp.MustCompile(`^[A-Za-z0-9_.:/-]+$`)
)

func ValidateArtifactName(name string) error {
	if name == "" {
		return fmt.Errorf("artifact name is required")
	}
	if !validArtifact.MatchString(name) {
		return fmt.Errorf("invalid artifact name: %q", name)
	}
	return nil
}

func ValidateFieldName(name string) error {
	if name == "*" {
		return nil
	}
	if !validFieldName.MatchString(name) {
		return fmt.Errorf("invalid field name: %q", name)
	}
	return nil
}

func ValidateFieldList(fields string) (string, error) {
	if strings.TrimSpace(fields) == "" {
		return "", fmt.Errorf("at least one field is required")
	}
	parts := strings.Split(fields, ",")
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
		if err := ValidateFieldName(parts[i]); err != nil {
			return "", err
		}
	}
	return strings.Join(parts, ", "), nil
}

func ValidateParamName(name string) error {
	if !validParamName.MatchString(name) {
		return fmt.Errorf("invalid parameter name: %q", name)
	}
	return nil
}

func VQLLiteral(v any) string {
	if v == nil {
		return "null"
	}
	switch val := v.(type) {
	case bool:
		if val {
			return "TRUE"
		}
		return "FALSE"
	case int:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
	case float64:
		return fmt.Sprintf("%v", val)
	case string:
		escaped := strings.ReplaceAll(val, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `'`, `\'`)
		return "'" + escaped + "'"
	default:
		b, _ := jsonMarshal(val)
		return VQLLiteral(string(b))
	}
}

func BuildEnvDict(params map[string]any) (string, error) {
	if len(params) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(params))
	for key, value := range params {
		if err := ValidateParamName(key); err != nil {
			return "", err
		}
		parts = append(parts, key+"="+VQLLiteral(value))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", "), nil
}
