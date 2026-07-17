package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdfranz/go-velociraptor-mcp/internal/raptor"
	"github.com/spf13/cobra"
)

var (
	flagVQLExportFile  string
	flagVQLExportMaxMB int
)

func init() {
	rootCmd.AddCommand(vqlCmd)
	vqlCmd.AddCommand(vqlRunCmd, vqlExportCmd)

	vqlExportCmd.Flags().StringVar(&flagVQLExportFile, "out", "", "output file path (required)")
	vqlExportCmd.Flags().IntVar(&flagVQLExportMaxMB, "max-mb", 100, "max file size in MB before rolling (max 1000)")
	_ = vqlExportCmd.MarkFlagRequired("out")
}

var vqlCmd = &cobra.Command{
	Use:   "vql",
	Short: "Read-only VQL operations",
}

var vqlRunCmd = &cobra.Command{
	Use:   "run <query>",
	Short: "Execute a read-only SELECT query",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := raptor.ValidateReadOnlyVQL(args[0]); err != nil {
			return err
		}
		rows, err := client.RunVQL(ctx(), args[0], orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

var vqlExportCmd = &cobra.Command{
	Use:   "export <query>",
	Short: "Stream a read-only SELECT query to a JSONL file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := raptor.ValidateReadOnlyVQL(args[0]); err != nil {
			return err
		}
		query := args[0]
		basePath := flagVQLExportFile
		maxFileMB := flagVQLExportMaxMB
		if maxFileMB <= 0 {
			maxFileMB = 100
		}
		if maxFileMB > 1000 {
			maxFileMB = 1000
		}
		maxFileBytes := int64(maxFileMB) * 1024 * 1024

		baseDir := filepath.Dir(basePath)
		baseName := filepath.Base(basePath)
		ext := filepath.Ext(baseName)
		stem := strings.TrimSuffix(baseName, ext)
		if ext == "" {
			ext = ".jsonl"
		}
		ts := time.Now().UTC().Format("20060102T150405Z")

		if baseDir != "" && baseDir != "." {
			if err := os.MkdirAll(baseDir, 0o755); err != nil {
				return fmt.Errorf("create output dir: %w", err)
			}
		}

		fileIndex := 1
		var exportedFiles []string
		totalRows := int64(0)

		newFile := func() (*os.File, *bufio.Writer, string, error) {
			name := filepath.Join(baseDir, fmt.Sprintf("%s_%s_%03d%s", stem, ts, fileIndex, ext))
			f, err := os.Create(name)
			if err != nil {
				return nil, nil, "", fmt.Errorf("create %s: %w", name, err)
			}
			exportedFiles = append(exportedFiles, name)
			fmt.Fprintf(os.Stderr, "writing %s\n", name)
			return f, bufio.NewWriterSize(f, 256*1024), name, nil
		}

		currentFile, currentWriter, currentName, err := newFile()
		if err != nil {
			return err
		}
		var currentBytes int64

		closeFile := func() error {
			if err := currentWriter.Flush(); err != nil {
				return err
			}
			return currentFile.Close()
		}

		start := time.Now()
		streamErr := client.StreamVQL(ctx(), query, orgID(), func(rows []map[string]any) error {
			for _, row := range rows {
				data, err := json.Marshal(row)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: marshal error, skipping row: %v\n", err)
					continue
				}
				if currentBytes > 0 && currentBytes+int64(len(data))+1 > maxFileBytes {
					if err := closeFile(); err != nil {
						return fmt.Errorf("flush %s: %w", currentName, err)
					}
					fileIndex++
					currentBytes = 0
					currentFile, currentWriter, currentName, err = newFile()
					if err != nil {
						return err
					}
				}
				currentWriter.Write(data)
				currentWriter.WriteByte('\n')
				currentBytes += int64(len(data)) + 1
				totalRows++
			}
			return nil
		})

		if ferr := closeFile(); ferr != nil && streamErr == nil {
			streamErr = fmt.Errorf("flush final file: %w", ferr)
		}
		if streamErr != nil {
			return streamErr
		}

		elapsed := time.Since(start)
		fmt.Fprintf(os.Stderr, "done: %d rows in %d file(s) (%dms)\n", totalRows, len(exportedFiles), elapsed.Milliseconds())
		return nil
	},
}
