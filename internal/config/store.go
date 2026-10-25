package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
	"github.com/zalando/go-keyring"
)

const keyringService = "dbdeck"

// Dir returns the config directory (dbdeck_CONFIG_DIR overrides it, handy for tests).
func Dir() (string, error) {
	if d := os.Getenv("dbdeck_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "dbdeck"), nil
}

func file() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "connections.toml"), nil
}

type fileFormat struct {
	Connections []Profile `toml:"connection"`
}

// Load reads the saved profiles. Passwords are not included; use LoadPassword.
func Load() ([]Profile, error) {
	path, err := file()
	if err != nil {
		return nil, err
	}
	var f fileFormat
	if _, err := toml.DecodeFile(path, &f); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	sort.SliceStable(f.Connections, func(i, j int) bool { return f.Connections[i].Name < f.Connections[j].Name })
	return f.Connections, nil
}

// Save writes the profiles atomically with 0600 permissions.
func Save(profiles []Profile) error {
	path, err := file()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	fh, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(fh).Encode(fileFormat{Connections: profiles}); err != nil {
		fh.Close()
		os.Remove(tmp)
		return err
	}
	if err := fh.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Upsert inserts or replaces a profile by name and saves. If oldName differs
// from p.Name the old entry is renamed.
func Upsert(oldName string, p Profile) error {
	list, err := Load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range list {
		if list[i].Name == oldName || list[i].Name == p.Name {
			list[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, p)
	}
	return Save(list)
}

// Remove deletes a profile by name (and its stored password).
func Remove(name string) error {
	list, err := Load()
	if err != nil {
		return err
	}
	out := make([]Profile, 0, len(list))
	for _, p := range list {
		if p.Name != name {
			out = append(out, p)
		}
	}
	_ = keyring.Delete(keyringService, name)
	return Save(out)
}

// SavePassword stores the password in the OS keychain. An error means no
// keychain is available; callers should tell the user the password was not saved.
func SavePassword(name, password string) error {
	if password == "" {
		_ = keyring.Delete(keyringService, name)
		return nil
	}
	return keyring.Set(keyringService, name, password)
}

// LoadPassword fetches a saved password; missing is not an error.
func LoadPassword(name string) string {
	pw, err := keyring.Get(keyringService, name)
	if err != nil {
		return ""
	}
	return pw
}
