package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/aymanbagabas/go-udiff"

	"github.com/shakfu/gilda/llm"
)

type Write struct{ Env }

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (Write) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "write",
		Description: "Create or replace a file with the given content. Creates parent directories.",
		Schema: schema([]string{"path", "content"}, map[string]any{
			"path":    prop("string", "File path, absolute or relative to the working directory."),
			"content": prop("string", "The complete file content."),
		}),
	}
}

func (Write) Label(raw json.RawMessage) string {
	var a writeArgs
	_ = decode(raw, &a)
	return "write " + a.Path
}

func (w Write) Run(_ context.Context, raw json.RawMessage) (Result, error) {
	a, err := parseWrite(raw)
	if err != nil {
		return Result{}, err
	}
	path := w.abs(a.Path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, err
	}
	if err := replace(path, []byte(a.Content)); err != nil {
		return Result{}, err
	}
	size := plural(len(a.Content), "byte")
	return Result{Output: "wrote " + size + " to " + a.Path, Summary: size}, nil
}

func parseWrite(raw json.RawMessage) (writeArgs, error) {
	var a writeArgs
	if err := decode(raw, &a, "path", "content"); err != nil {
		return a, err
	}
	if a.Path == "" {
		return a, fmt.Errorf("path is required")
	}
	return a, nil
}

// Preview returns a unified diff against the file the write replaces, following a symlink as
// Run does; a new file diffs against /dev/null. A file over Limits.DiffBytes or holding a NUL
// byte gets a one-line summary instead: Run never reads the old file, so without a bound a
// preview could cost more than the write.
func (w Write) Preview(raw json.RawMessage) (string, error) {
	a, err := parseWrite(raw)
	if err != nil {
		return "", err
	}
	f, info, err := openRegular(w.abs(a.Path), a.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return udiff.Unified("/dev/null", a.Path, "", a.Content), nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	diffCap := w.limits().DiffBytes
	data, err := io.ReadAll(io.LimitReader(f, int64(diffCap)+1))
	if err != nil {
		return "", err
	}
	if len(data) > diffCap {
		return fmt.Sprintf("replaces %s (%s) with %s; too large to diff", a.Path,
			plural(int(info.Size()), "byte"), plural(len(a.Content), "byte")), nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return fmt.Sprintf("replaces binary %s (%s) with %s", a.Path,
			plural(len(data), "byte"), plural(len(a.Content), "byte")), nil
	}
	if string(data) == a.Content {
		return "content unchanged", nil
	}
	return udiff.Unified(a.Path, a.Path, string(data), a.Content), nil
}

// replace writes data to path through a temporary file and a rename, so a crash or a full
// disk never leaves half a file where the only copy was. It follows a symlink to its target
// and keeps an existing file's mode.
func replace(path string, data []byte) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".gilda-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
