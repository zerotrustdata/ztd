// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package report

import (
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
)

// Markdown renders the coverage table (rows × postures) and the recall table.
func (r *Report) Markdown() string {
	var b strings.Builder
	writeCoverage(&b, r.Cells)
	b.WriteString("\n")
	writeRecall(&b, r.Recall)
	return b.String()
}

// writeCoverage writes one row per (catalogue row, ingress) and one column per posture.
func writeCoverage(b *strings.Builder, cells []Cell) {
	type rowKey struct {
		row     int
		ingress cases.Ingress
	}
	byRow := make(map[rowKey]map[string]Cell)
	var order []rowKey
	for _, c := range cells {
		k := rowKey{c.Row, c.Ingress}
		if _, ok := byRow[k]; !ok {
			byRow[k] = make(map[string]Cell)
			order = append(order, k)
		}
		byRow[k][c.Posture] = c
	}

	b.WriteString("| Row | Ingress |")
	for _, col := range postureColumns {
		b.WriteString(" " + col.name + " |")
	}
	b.WriteString("\n| --- | --- |")
	for range postureColumns {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")

	for _, k := range order {
		fmt.Fprintf(b, "| %d | %s |", k.row, k.ingress)
		for _, col := range postureColumns {
			text := "-"
			if c, ok := byRow[k][col.name]; ok {
				text = renderCell(c)
			}
			b.WriteString(" " + text + " |")
		}
		b.WriteString("\n")
	}
}

// renderCell is the status, followed by the reason in parentheses when there is one.
func renderCell(c Cell) string {
	if c.Reason == "" {
		return string(c.Status)
	}
	return fmt.Sprintf("%s (%s)", c.Status, escapeCell(c.Reason))
}

// writeRecall writes one row per recall line. An empty bundle hash renders as "-".
func writeRecall(b *strings.Builder, recall []Recall) {
	b.WriteString("| Category | Posture | Ingress | Blocked | Total | Doctrine bundle hash |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, r := range recall {
		hash := r.DoctrineBundleHash
		if hash == "" {
			hash = "-"
		}
		fmt.Fprintf(b, "| %s | %s | %s | %d | %d | %s |\n",
			r.Category, r.Posture, r.Ingress, r.Blocked, r.Total, escapeCell(hash))
	}
}

// escapeCell keeps a value from closing its Markdown table cell.
func escapeCell(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}
