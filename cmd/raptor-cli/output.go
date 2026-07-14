package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

func printRows(rows []map[string]any) {
	if len(rows) == 0 {
		fmt.Println("(no results)")
		return
	}
	switch flagOutput {
	case "json":
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(b))
	default:
		printTable(rows)
	}
}

func printTable(rows []map[string]any) {
	keys := columnOrder(rows[0])
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(keys, "\t"))
	fmt.Fprintln(w, strings.Join(dashes(keys), "\t"))
	for _, row := range rows {
		vals := make([]string, len(keys))
		for i, k := range keys {
			vals[i] = cellStr(row[k])
		}
		fmt.Fprintln(w, strings.Join(vals, "\t"))
	}
	w.Flush()
}

func columnOrder(row map[string]any) []string {
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dashes(keys []string) []string {
	d := make([]string, len(keys))
	for i, k := range keys {
		d[i] = strings.Repeat("-", len(k))
	}
	return d
}

func cellStr(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		s := strings.ReplaceAll(strings.TrimSpace(val), "\n", " ")
		if len(s) > 80 {
			s = s[:77] + "..."
		}
		return s
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%v", val)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
