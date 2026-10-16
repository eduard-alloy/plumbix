// Package diff parses git's unified diff output into files, hunks and
// numbered lines, the shape the API returns and the web interface draws.
package diff

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// Line types.
const (
	Context = "context"
	Add     = "add"
	Del     = "del"
)

// File statuses. Renames are not detected, so a rename is a delete and
// an add.
const (
	Added    = "added"
	Modified = "modified"
	Deleted  = "deleted"
)

// Line is one line of a hunk, with its number on the old side, the new
// side, or both.
type Line struct {
	Type string `json:"type"`
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
	Text string `json:"text"`
}

// Hunk is one changed region.
type Hunk struct {
	OldStart int    `json:"old_start"`
	OldLines int    `json:"old_lines"`
	NewStart int    `json:"new_start"`
	NewLines int    `json:"new_lines"`
	Header   string `json:"header"`
	Lines    []Line `json:"lines"`
}

// File is one changed file.
type File struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Binary    bool   `json:"binary"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Hunks     []Hunk `json:"hunks,omitempty"`
}

// Diff is the change between two commits.
type Diff struct {
	Base      string `json:"base"`
	Head      string `json:"head"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Truncated bool   `json:"truncated"`
	Files     []File `json:"files"`
}

// Total sums the files' additions and deletions into the diff.
func (d *Diff) Total() {
	d.Additions, d.Deletions = 0, 0
	for _, f := range d.Files {
		d.Additions += f.Additions
		d.Deletions += f.Deletions
	}
}

// Stat returns a copy without hunks.
func (d Diff) Stat() Diff {
	out := d
	out.Files = make([]File, len(d.Files))
	for i, f := range d.Files {
		f.Hunks = nil
		out.Files[i] = f
	}
	return out
}

// Parse reads the output of git diff (without renames) into files.
func Parse(unified []byte) ([]File, error) {
	files := []File{}
	var cur *File
	var hunk *Hunk
	var oldN, newN int
	sc := bufio.NewScanner(bytes.NewReader(unified))
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files = append(files, File{Status: Modified, Path: pathFromHeader(line)})
			cur, hunk = &files[len(files)-1], nil
		case cur == nil:
			return nil, fmt.Errorf("line %d: %q before any file header", lineNo, line)
		case strings.HasPrefix(line, "--- "):
			if line == "--- /dev/null" {
				cur.Status = Added
			}
		case strings.HasPrefix(line, "+++ "):
			if line == "+++ /dev/null" {
				cur.Status = Deleted
			} else if p, ok := strings.CutPrefix(strings.TrimRight(line, "\t"), "+++ b/"); ok {
				cur.Path = p
			}
		case strings.HasPrefix(line, "Binary files "):
			cur.Binary = true
			if strings.HasPrefix(line, "Binary files /dev/null ") {
				cur.Status = Added
			} else if strings.HasSuffix(line, " and /dev/null differ") {
				cur.Status = Deleted
			}
		case strings.HasPrefix(line, "@@ "):
			h, err := parseHunkHeader(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			cur.Hunks = append(cur.Hunks, h)
			hunk = &cur.Hunks[len(cur.Hunks)-1]
			oldN, newN = h.OldStart, h.NewStart
		case hunk == nil:
			// Extended headers: index, mode, similarity. Nothing to keep.
		case strings.HasPrefix(line, "\\"):
			// "\ No newline at end of file": not a line of the file.
		case strings.HasPrefix(line, " "):
			hunk.Lines = append(hunk.Lines, Line{Type: Context, Old: oldN, New: newN, Text: line[1:]})
			oldN++
			newN++
		case strings.HasPrefix(line, "+"):
			hunk.Lines = append(hunk.Lines, Line{Type: Add, New: newN, Text: line[1:]})
			newN++
			cur.Additions++
		case strings.HasPrefix(line, "-"):
			hunk.Lines = append(hunk.Lines, Line{Type: Del, Old: oldN, Text: line[1:]})
			oldN++
			cur.Deletions++
		case line == "":
			// An empty context line with its leading space stripped.
			hunk.Lines = append(hunk.Lines, Line{Type: Context, Old: oldN, New: newN})
			oldN++
			newN++
		default:
			return nil, fmt.Errorf("line %d: unexpected %q inside a hunk", lineNo, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

// pathFromHeader reads the path from "diff --git a/p b/p". The +++ line,
// when there is one, overrides it; this handles files with no hunks.
func pathFromHeader(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.Index(rest, " b/"); i >= 0 {
		return strings.TrimPrefix(rest[:i], "a/")
	}
	return rest
}

// parseHunkHeader reads "@@ -old[,n] +new[,n] @@ header".
func parseHunkHeader(line string) (Hunk, error) {
	rest := strings.TrimPrefix(line, "@@ ")
	ranges, header, ok := strings.Cut(rest, " @@")
	if !ok {
		return Hunk{}, fmt.Errorf("malformed hunk header %q", line)
	}
	oldR, newR, ok := strings.Cut(ranges, " ")
	if !ok || !strings.HasPrefix(oldR, "-") || !strings.HasPrefix(newR, "+") {
		return Hunk{}, fmt.Errorf("malformed hunk header %q", line)
	}
	h := Hunk{Header: strings.TrimPrefix(header, " "), Lines: []Line{}}
	var err error
	if h.OldStart, h.OldLines, err = parseRange(oldR[1:]); err != nil {
		return Hunk{}, fmt.Errorf("malformed hunk header %q", line)
	}
	if h.NewStart, h.NewLines, err = parseRange(newR[1:]); err != nil {
		return Hunk{}, fmt.Errorf("malformed hunk header %q", line)
	}
	return h, nil
}

// parseRange reads "start[,count]"; a missing count means one line.
func parseRange(s string) (int, int, error) {
	startS, countS, hasCount := strings.Cut(s, ",")
	start, err := strconv.Atoi(startS)
	if err != nil {
		return 0, 0, err
	}
	count := 1
	if hasCount {
		if count, err = strconv.Atoi(countS); err != nil {
			return 0, 0, err
		}
	}
	return start, count, nil
}
