// Package output renders command results as a human readable table or as
// machine readable JSON/YAML.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v3"
)

// Format is an output encoding selected by the global -o/--output flag.
type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatYAML  Format = "yaml"
)

// Formats lists every valid --output value, for help text and validation.
func Formats() []string {
	return []string{string(FormatTable), string(FormatJSON), string(FormatYAML)}
}

// ParseFormat validates a user supplied format string.
func ParseFormat(s string) (Format, error) {
	switch Format(strings.ToLower(strings.TrimSpace(s))) {
	case FormatTable:
		return FormatTable, nil
	case FormatJSON:
		return FormatJSON, nil
	case FormatYAML:
		return FormatYAML, nil
	default:
		return "", fmt.Errorf("unknown output format %q (want one of: %s)", s, strings.Join(Formats(), ", "))
	}
}

// Structured reports whether the format is machine readable, which callers use
// to suppress decorative output.
func (f Format) Structured() bool { return f == FormatJSON || f == FormatYAML }

// Table is a simple column-aligned grid.
type Table struct {
	Headers []string
	Rows    [][]string
}

// AddRow appends a row, converting each cell with fmt.Sprint.
func (t *Table) AddRow(cells ...any) {
	row := make([]string, 0, len(cells))
	for _, c := range cells {
		row = append(row, fmt.Sprint(c))
	}
	t.Rows = append(t.Rows, row)
}

// WriteTable renders the table with tab-aligned columns.
func WriteTable(w io.Writer, t Table) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if len(t.Headers) > 0 {
		if _, err := fmt.Fprintln(tw, strings.Join(t.Headers, "\t")); err != nil {
			return err
		}
	}
	for _, row := range t.Rows {
		if _, err := fmt.Fprintln(tw, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// Write emits v in the requested format. For FormatTable the caller supplies a
// pre-built table, since the flat grid rarely maps 1:1 onto the data shape.
func Write(w io.Writer, f Format, v any, table Table) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		if err := enc.Encode(v); err != nil {
			return err
		}
		return enc.Close()
	default:
		return WriteTable(w, table)
	}
}
