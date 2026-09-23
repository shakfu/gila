package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/shakfu/gila/llm"
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
	var a writeArgs
	if err := decode(raw, &a, "path", "content"); err != nil {
		return Result{}, err
	}
	if a.Path == "" {
		return Result{}, fmt.Errorf("path is required")
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
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".gila-*")
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
