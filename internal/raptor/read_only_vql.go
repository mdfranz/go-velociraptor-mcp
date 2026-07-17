package raptor

import (
	"fmt"
	"strings"
	"unicode"
)

// ValidateReadOnlyVQL permits one SELECT statement and rejects statement
// sequences before they are sent to the Velociraptor server.
func ValidateReadOnlyVQL(query string) error {
	start, err := skipVQLSpaceAndComments(query, 0)
	if err != nil {
		return err
	}
	if start == len(query) {
		return fmt.Errorf("query is required")
	}
	if !hasVQLKeyword(query, start, "SELECT") {
		return fmt.Errorf("only a single SELECT statement is allowed")
	}

	inString := byte(0)
	escaped := false
	for i := start; i < len(query); i++ {
		ch := query[i]
		if inString != 0 {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == inString {
				inString = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			inString = ch
			continue
		}
		if ch == '/' && i+1 < len(query) && query[i+1] == '*' {
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				return fmt.Errorf("unterminated VQL comment")
			}
			i += end + 3
			continue
		}
		if (ch == '/' && i+1 < len(query) && query[i+1] == '/') ||
			(ch == '-' && i+1 < len(query) && query[i+1] == '-') ||
			ch == '#' {
			for i < len(query) && query[i] != '\n' {
				i++
			}
			continue
		}
		if ch == ';' {
			next, err := skipVQLSpaceAndComments(query, i+1)
			if err != nil {
				return err
			}
			if next != len(query) {
				return fmt.Errorf("multiple VQL statements are not allowed")
			}
			break
		}
	}
	if inString != 0 {
		return fmt.Errorf("unterminated VQL string")
	}
	return nil
}

func skipVQLSpaceAndComments(query string, start int) (int, error) {
	for start < len(query) {
		if unicode.IsSpace(rune(query[start])) {
			start++
			continue
		}
		if query[start] == '#' ||
			(start+1 < len(query) && query[start] == '/' && query[start+1] == '/') ||
			(start+1 < len(query) && query[start] == '-' && query[start+1] == '-') {
			for start < len(query) && query[start] != '\n' {
				start++
			}
			continue
		}
		if start+1 < len(query) && query[start] == '/' && query[start+1] == '*' {
			end := strings.Index(query[start+2:], "*/")
			if end < 0 {
				return 0, fmt.Errorf("unterminated VQL comment")
			}
			start += end + 4
			continue
		}
		break
	}
	return start, nil
}

func hasVQLKeyword(query string, start int, keyword string) bool {
	if len(query)-start < len(keyword) ||
		!strings.EqualFold(query[start:start+len(keyword)], keyword) {
		return false
	}
	end := start + len(keyword)
	return end == len(query) || !isVQLIdentifierChar(rune(query[end]))
}

func isVQLIdentifierChar(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_'
}
