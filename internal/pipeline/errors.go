package pipeline

import (
	"fmt"
	"strings"
)

// Error is one problem found in a pipeline file.
type Error struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// ValidationError lists every problem found in a pipeline file.
type ValidationError struct {
	Errors []Error
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	b.WriteString("invalid pipeline:")
	for _, pe := range e.Errors {
		b.WriteString("\n  ")
		if pe.Line > 0 {
			fmt.Fprintf(&b, "line %d: ", pe.Line)
		}
		if pe.Path != "" {
			b.WriteString(pe.Path + ": ")
		}
		b.WriteString(pe.Message)
	}
	return b.String()
}
