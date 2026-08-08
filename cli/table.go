package cli

import (
	"fmt"
	"strings"
)

// table is a minimal column-aligned text table for CLI output.
type table struct {
	headers []string
	rows    [][]string
}

func newTable(headers ...string) *table {
	return &table{headers: headers}
}

func (t *table) row(cells ...string) {
	t.rows = append(t.rows, cells)
}

// print renders the table with fixed-width columns.
func (t *table) print() {
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = len(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			if i < len(widths) && len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	pad := func(s string, w int) string {
		if len(s) >= w {
			return s
		}
		return s + strings.Repeat(" ", w-len(s))
	}
	var hdr []string
	for i, h := range t.headers {
		hdr = append(hdr, pad(h, widths[i]))
	}
	fmt.Println(strings.Join(hdr, "  "))
	for _, r := range t.rows {
		var cells []string
		for i, c := range r {
			if i < len(widths) {
				cells = append(cells, pad(c, widths[i]))
			} else {
				cells = append(cells, c)
			}
		}
		fmt.Println(strings.Join(cells, "  "))
	}
}
