package state

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/shakfu/gila/permission"
)

// SettingsFile is the name of the user's settings in the config directory.
const SettingsFile = "settings.toml"

// Settings is what the user sets in settings.toml:
//
//	[permissions]
//	mode = "ask"
//	secrets = ["*.pem", "secrets/*"]
//	protected = ["go.sum", "migrations"]
//
// The mode applies when neither --permissions nor GILA_PERMISSIONS sets one. The patterns add
// to the built-in ones and cannot lift them.
type Settings struct {
	Permissions Permissions `toml:"permissions"`
}

type Permissions struct {
	// Mode is auto, ask, all or read-only; empty leaves the default, auto.
	Mode             permission.Mode `toml:"mode"`
	permission.Rules                 // secrets and protected
}

// LoadSettings reads settings.toml from dir; an empty dir means ConfigDir. A missing file is
// no settings. An unknown key is an error, so a misspelled one is not silently ignored.
func LoadSettings(dir string) (Settings, error) {
	if dir == "" {
		dir = ConfigDir()
	}
	path := filepath.Join(dir, SettingsFile)
	var s Settings
	meta, err := toml.DecodeFile(path, &s)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Settings{}, nil
	case err != nil:
		return Settings{}, fmt.Errorf("%s: %w", path, err)
	}
	if extra := meta.Undecoded(); len(extra) > 0 {
		keys := make([]string, len(extra))
		for i, k := range extra {
			keys[i] = k.String()
		}
		return Settings{}, fmt.Errorf("%s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	if s.Permissions.Mode != "" {
		if _, err := permission.Parse(string(s.Permissions.Mode)); err != nil {
			return Settings{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	if err := s.Permissions.Validate(); err != nil {
		return Settings{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}
