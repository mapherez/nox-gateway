package state

import (
	"encoding/json"
	"errors"
	"os"
)

// Cached registry/producer state is not authority. Restart always reconciles it
// with Backend; it never contains personal keys or delegated user/probe tokens.
func (s *Store) LoadRegistry() ([]byte, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	b, e := s.readPrivate("registry.json", 8<<20)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	return b, e
}
func (s *Store) SaveRegistry(b []byte) error {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	if len(b) > 8<<20 || !json.Valid(b) {
		return ErrInvalid
	}
	return s.writeAtomic("registry.json", b)
}
