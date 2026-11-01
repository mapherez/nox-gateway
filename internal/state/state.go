// Package state owns the Gateway's private local files, not the Suite registry.
package state

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"

	noxmcp "github.com/mapherez/nox-mcp/go"
)

var (
	ErrLocked  = errors.New("state directory is already in use by another Gateway")
	ErrUnsafe  = errors.New("state requires a private directory (0700) and regular private files (0600), without symlinks")
	ErrInvalid = errors.New("persistent identity is invalid; restore the original state instead of regenerating it")
	ErrIO      = errors.New("persistent state could not be read or written")
)

var serverIDPattern = regexp.MustCompile(`^nox-gateway-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type Identity struct {
	SchemaVersion int    `json:"schemaVersion"`
	ServerID      string `json:"serverId"`
}

type Store struct {
	root      *os.Root
	lock      *os.File
	identity  Identity
	closeOnce sync.Once
	closeErr  error
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, ErrIO
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, ErrIO
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrUnsafe
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrIO
	}
	s := &Store{root: root}
	ok := false
	defer func() {
		if !ok {
			_ = s.Close()
		}
	}()
	if err := privateFile(root, ".lock", true); err != nil {
		return nil, err
	}
	s.lock, err = root.OpenFile(".lock", os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, ErrIO
	}
	if err := lockFile(s.lock); err != nil {
		return nil, err
	}
	if err := privateFile(root, "identity.json", true); err != nil {
		return nil, err
	}
	f, err := root.Open("identity.json")
	if errors.Is(err, os.ErrNotExist) {
		s.identity, err = createIdentity(root)
	} else if err != nil {
		return nil, ErrIO
	} else {
		s.identity, err = readIdentity(f)
		_ = f.Close()
	}
	if err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

func privateFile(root *os.Root, name string, absentOK bool) error {
	info, err := root.Lstat(name)
	if absentOK && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrIO
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return ErrUnsafe
	}
	return nil
}

func readIdentity(r io.Reader) (Identity, error) {
	const maxIdentityBytes = 4096
	data, err := io.ReadAll(io.LimitReader(r, maxIdentityBytes+1))
	if err != nil {
		return Identity{}, ErrIO
	}
	if _, err := noxmcp.ParseSuiteJSONWithLimit(data, maxIdentityBytes); err != nil {
		return Identity{}, ErrInvalid
	}
	var id Identity
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&id); err != nil || id.SchemaVersion != 1 || !serverIDPattern.MatchString(id.ServerID) {
		return Identity{}, ErrInvalid
	}
	return id, nil
}

func createIdentity(root *os.Root) (Identity, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Identity{}, ErrIO
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	id := Identity{SchemaVersion: 1, ServerID: fmt.Sprintf("nox-gateway-%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])}
	data, err := json.Marshal(id)
	if err != nil {
		return Identity{}, ErrIO
	}
	name := ".identity-" + rand.Text()
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Identity{}, ErrIO
	}
	defer root.Remove(name)
	_, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return Identity{}, ErrIO
	}
	if err := root.Rename(name, "identity.json"); err != nil {
		return Identity{}, ErrIO
	}
	dir, err := root.Open(".")
	if err != nil {
		return Identity{}, ErrIO
	}
	syncErr = dir.Sync()
	closeErr = dir.Close()
	if syncErr != nil || closeErr != nil {
		return Identity{}, ErrIO
	}
	return id, nil
}

// Identity returns a value copy; callers cannot mutate persisted identity.
func (s *Store) Identity() Identity { return s.identity }

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		if s.lock != nil {
			s.closeErr = s.lock.Close()
		}
		if s.root != nil {
			s.closeErr = errors.Join(s.closeErr, s.root.Close())
		}
	})
	return s.closeErr
}
