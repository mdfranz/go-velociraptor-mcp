package raptor

import "testing"

func TestValidateFieldList(t *testing.T) {
	got, err := ValidateFieldList("Name, ProcInfo.Pid, *")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Name, ProcInfo.Pid, *"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestValidateFieldListRejectsVQLExpressions(t *testing.T) {
	invalid := []string{
		"",
		"Name AS Process",
		"Name, now() AS Timestamp",
		"Name,,Pid",
	}
	for _, fields := range invalid {
		t.Run(fields, func(t *testing.T) {
			if _, err := ValidateFieldList(fields); err == nil {
				t.Fatalf("expected %q to be rejected", fields)
			}
		})
	}
}
