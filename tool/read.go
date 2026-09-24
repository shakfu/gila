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
	"syscall"

	"github.com/shakfu/gilda/llm"
)

type Read struct{ Env }

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (r Read) Spec() llm.ToolSpec {
	n := r.limits().ReadLines
	return llm.ToolSpec{
		Name:        "read",
		Description: fmt.Sprintf("Read a text file as numbered lines. Returns at most %d lines; use offset and limit for more.", n),
		Schema: schema([]string{"path"}, map[string]any{
			"path":   prop("string", "File path, absolute or relative to the working directory."),
			"offset": prop("integer", "First line, 1-based. Default 1."),
			"limit":  prop("integer", fmt.Sprintf("Maximum lines. Default %d.", n)),
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
func (r Read) Run(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a readArgs
	if err := decode(raw, &a); err != nil {
		return Result{}, err
	}
	if a.Path == "" {
		return Result{}, fmt.Errorf("path is required")
	}
	f, info, err := openRegular(r.abs(a.Path), a.Path)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64<<10)
	if head, _ := br.Peek(8 << 10); bytes.IndexByte(head, 0) >= 0 {
		return Result{Output: fmt.Sprintf("%s is binary, %d bytes", a.Path, info.Size()), Summary: "binary"}, nil
	}

	start := max(a.Offset, 1)
	lim := r.limits()
	limit := lim.ReadLines
	if a.Limit > 0 {
		limit = min(a.Limit, lim.ReadLines)
	}
	var out strings.Builder
	total, shown, cut := 0, 0, false
	for {
		// ReadLineBytes bounds one line; minified files otherwise fill the result with one line.
		line, long, err := readLine(br, lim.ReadLineBytes)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, err
		}
		total++
		if total%4096 == 0 && ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		// Lines past the window are still counted, so the footer can say how many are left.
		if total >= start && shown < limit && out.Len() < lim.OutputCap {
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
		fmt.Fprintf(&out, "... long lines were cut at %d bytes\n", lim.ReadLineBytes)
	}
	return Result{Output: out.String(), Summary: plural(shown, "line")}, nil
}

// openRegular opens path for reading and refuses anything but a regular file: a device has no
// end, and a directory fails with a less helpful error. O_NONBLOCK keeps opening a FIFO from
// waiting for a writer; the check is on the opened file, so a swap after it cannot slip past.
func openRegular(path, name string) (*os.File, os.FileInfo, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", name)
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

// readLine returns one line without its newline or carriage return, cut at lineCap bytes, and
// whether it was cut. It never holds more than lineCap bytes of a line.
func readLine(br *bufio.Reader, lineCap int) (string, bool, error) {
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
