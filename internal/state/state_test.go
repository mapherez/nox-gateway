package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentitySurvivesRestartAndStateIsExclusive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	id := s.Identity()
	if id.SchemaVersion != 1 || !serverIDPattern.MatchString(id.ServerID) {
		t.Fatal("invalid generated identity")
	}
	if second, err := Open(dir); !errors.Is(err, ErrLocked) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second process acquired state: %v", err)
	}
	copy := s.Identity()
	copy.ServerID = "changed"
	if s.Identity() != id {
		t.Fatal("identity was mutable through a copy")
	}
	for _, name := range []string{"", ".lock", "identity.json"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("state is not private: %s", name)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.Identity() != id {
		t.Fatal("ordinary restart changed identity")
	}
	other, err := Open(filepath.Join(t.TempDir(), "other"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.Identity() == id {
		t.Fatal("independent installations shared an identity")
	}
}

func TestInvalidIdentityNeverRegeneratesOrOverwrites(t *testing.T) {
	valid := Identity{SchemaVersion: 1, ServerID: "nox-gateway-00000000-0000-4000-8000-000000000000"}
	encoded, _ := json.Marshal(valid)
	cases := map[string]string{
		"empty": "", "broken": "{", "wrongVersion": strings.Replace(string(encoded), ":1", ":2", 1),
		"wrongID":      `{"schemaVersion":1,"serverId":"not-a-server-id"}`,
		"duplicate":    `{"schemaVersion":1,"schemaVersion":1,"serverId":"nox-gateway-00000000-0000-4000-8000-000000000000"}`,
		"unknownField": strings.TrimSuffix(string(encoded), "}") + `,"secret":"never-print"}`,
		"trailing":     string(encoded) + ` {}`, "oversized": string(encoded) + strings.Repeat(" ", 4096),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			dir := privateDir(t)
			path := filepath.Join(dir, "identity.json")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if s, err := Open(dir); !errors.Is(err, ErrInvalid) {
				if s != nil {
					_ = s.Close()
				}
				t.Fatalf("accepted invalid identity: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != data {
				t.Fatal("invalid identity was overwritten")
			}
			// A failed open must release the directory lock too.
			if err := os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			_ = s.Close()
		})
	}
}

func TestUnsafeStateFilesAndDirectoriesAreRejected(t *testing.T) {
	for _, name := range []string{"identity.json", ".lock"} {
		t.Run(name+"Symlink", func(t *testing.T) {
			dir := privateDir(t)
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, []byte("do not modify"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			if s, err := Open(dir); !errors.Is(err, ErrUnsafe) {
				if s != nil {
					_ = s.Close()
				}
				t.Fatalf("followed a state symlink: %v", err)
			}
			data, _ := os.ReadFile(target)
			if string(data) != "do not modify" {
				t.Fatal("symlink target was modified")
			}
		})
		t.Run(name+"Public", func(t *testing.T) {
			dir := privateDir(t)
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
			if s, err := Open(dir); !errors.Is(err, ErrUnsafe) {
				if s != nil {
					_ = s.Close()
				}
				t.Fatalf("accepted public state: %v", err)
			}
		})
	}
	t.Run("directory", func(t *testing.T) {
		dir := privateDir(t)
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if s, err := Open(dir); !errors.Is(err, ErrUnsafe) {
			if s != nil {
				_ = s.Close()
			}
			t.Fatalf("accepted public directory: %v", err)
		}
	})
	t.Run("directorySymlink", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(t.TempDir(), link); err != nil {
			t.Fatal(err)
		}
		if s, err := Open(link); !errors.Is(err, ErrUnsafe) {
			if s != nil {
				_ = s.Close()
			}
			t.Fatalf("followed directory symlink: %v", err)
		}
	})
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private-state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
