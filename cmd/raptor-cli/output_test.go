package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestPrintTableWithColumns(t *testing.T) {
	row := map[string]any{
		"state":                   "RUNNING",
		"uploaded_bytes":          0,
		"hunt_description":        "Test",
		"clients_scheduled":       2,
		"created":                 "2026-07-16T16:04:29Z",
		"creator":                 "raptoradmin",
		"hunt_id":                 "H.D9CG23F0AUEPE",
		"clients_with_errors":     0,
		"clients_with_results":    0,
		"clients_without_results": 0,
		"expires":                 "2026-07-23T16:03:25Z",
		"rows":                    0,
		"started":                 "2026-07-16T16:04:54Z",
	}
	columns := []string{
		"hunt_id",
		"created",
		"creator",
		"hunt_description",
		"state",
		"clients_scheduled",
	}

	originalStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	printTableWithColumns([]map[string]any{row}, columns)
	_ = write.Close()
	os.Stdout = originalStdout
	output, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	_ = read.Close()

	header := strings.Fields(strings.SplitN(string(output), "\n", 2)[0])
	if strings.Join(header, " ") != strings.Join(columns, " ") {
		t.Fatalf("table header = %v, want %v", header, columns)
	}
}

func TestColumnOrderPutsIDFirst(t *testing.T) {
	row := map[string]any{
		"AgentVersion": "0.77.1",
		"FirstSeen":    "2026-07-14T20:40:09Z",
		"client_id":    "C.5449f237e47ac98e",
		"Hostname":     "pi5-8gb-5a7f55",
		"OS":           "ubuntu26.04",
	}

	got := columnOrder(row)
	if got[0] != "client_id" {
		t.Fatalf("first column = %q, want %q", got[0], "client_id")
	}
}
