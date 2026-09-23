package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shakfu/gila/llm"
)

const (
	readLines = 2000
	// lineCap bounds one line; minified files otherwise fill the result with one line.
	lineCap = 2000
)

type Read struct{ Env }

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (Read) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "read",
		Description: "Read a text file as numbered lines. Returns at most 2000 lines; use offset and limit for more.",
		Schema: schema([]string{"path"}, map[string]any{
			"path":   prop("string", "File path, absolute or relative to the working directory."),
			"offset": prop("integer", "First line, 1-based. Default 1."),
			"limit":  prop("integer", "Maximum lines. Default 2000."),
		}),
	}
}

func (Read) Label(raw json.RawMessage) string {
	var a readArgs
	_ = decode(raw, &a)
	if a.Offset > 0 || a.Limit > 0 {
		start := max(a.Offset, 1)
		end := "end"
		if a.Limit > 0 {
			end = fmt.Sprint(start + a.Limit - 1)
		}
		return fmt.Sprintf("read %s:%d-%s", a.Path, start, end)
	}
	return "read " + a.Path
}

// Run streams the file, so memory follows the lines returned rather than the file size.
func (r Read) Run(_ context.Context, raw json.RawMessage) (Result, error) {
	var a readArgs
	if err := decode(raw, &a); err != nil {
		return Result{}, err
	}
	if a.Path == "" {
		return Result{}, fmt.Errorf("path is required")
	}
	f, err := os.Open(r.abs(a.Path))
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	// A device has no end, and a directory fails with a less helpful error.
	if !info.Mode().IsRegular() {
		return Result{}, fmt.Errorf("%s is not a regular file", a.Path)
	}

	br := bufio.NewReaderSize(f, 64<<10)
	if head, _ := br.Peek(8 << 10); bytes.IndexByte(head, 0) >= 0 {
		return Result{Output: fmt.Sprintf("%s is binary, %d bytes", a.Path, info.Size()), Summary: "binary"}, nil
	}

	start := max(a.Offset, 1)
	limit := readLines
	if a.Limit > 0 {
		limit = min(a.Limit, readLines)
	}
	var out strings.Builder
	total, shown, cut := 0, 0, false
	for {
		line, long, err := readLine(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, err
		}
		total++
		// Lines past the window are still counted, so the footer can say how many are left.
		if total >= start && shown < limit && out.Len() < OutputCap {
			fmt.Fprintf(&out, "%6d\t%s\n", total, line)
			shown++
			cut = cut || long
		}
	}
	if shown == 0 {
		msg := fmt.Sprintf("%s has %d lines; none at offset %d", a.Path, total, start)
		return Result{Output: msg, Summary: plural(0, "line")}, nil
	}
	if last := start + shown - 1; last < total {
		fmt.Fprintf(&out, "... %s not shown; continue with offset %d\n", plural(total-last, "line"), last+1)
	}
	if cut {
		fmt.Fprintf(&out, "... long lines were cut at %d bytes\n", lineCap)
	}
	return Result{Output: out.String(), Summary: plural(shown, "line")}, nil
}

// readLine returns one line without its newline or carriage return, cut at lineCap, and
// whether it was cut. It never holds more than lineCap bytes of a line.
func readLine(br *bufio.Reader) (string, bool, error) {
	var buf []byte
	long := false
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && (buf != nil || long) {
				break
			}
			return "", false, err
		}
		if room := lineCap - len(buf); room > 0 {
			if len(chunk) > room {
				chunk, long = chunk[:room], true
			}
			buf = append(buf, chunk...)
		} else {
			long = true
		}
		if !isPrefix {
			break
		}
	}
	return string(bytes.TrimSuffix(buf, []byte("\r"))), long, nil
}
