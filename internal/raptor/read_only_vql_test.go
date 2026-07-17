package raptor

import "testing"

func TestValidateReadOnlyVQL(t *testing.T) {
	valid := []string{
		"SELECT * FROM clients()",
		" /* comment */\nselect Name from foo();",
		"SELECT * FROM foo() WHERE message = ';'",
	}
	for _, query := range valid {
		t.Run(query, func(t *testing.T) {
			if err := ValidateReadOnlyVQL(query); err != nil {
				t.Fatalf("ValidateReadOnlyVQL(%q) = %v", query, err)
			}
		})
	}

	invalid := []string{
		"",
		"LET x = SELECT * FROM clients() SELECT * FROM x",
		"LET x <= collect_client()",
		"UPDATE foo",
		"SELECT * FROM foo(); SELECT * FROM bar()",
		"SELECT 'unterminated",
		"/* unterminated",
	}
	for _, query := range invalid {
		t.Run(query, func(t *testing.T) {
			if err := ValidateReadOnlyVQL(query); err == nil {
				t.Fatalf("ValidateReadOnlyVQL(%q) unexpectedly succeeded", query)
			}
		})
	}
}
