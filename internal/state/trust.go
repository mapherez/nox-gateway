package state

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"os"
)

const trustFile = "trust.enc"
const masterFile = "master.key"
const maxTrustBytes = 2 << 20
const trustMagic = "NOXTRUST1"

// LoadTrust/SaveTrust are only for service trust and recovery journals. Personal
// keys and delegated user tokens must never be written to this store.
func (s *Store) LoadTrust() ([]byte, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	data, err := s.readPrivate(trustFile, maxTrustBytes+64)
	if errors.Is(err, os.ErrNotExist) {
		if _, e := s.root.Lstat(masterFile); !errors.Is(e, os.ErrNotExist) {
			return nil, ErrInvalid
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	key, err := s.trustKey(false)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrInvalid
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalid
	}
	header := len(trustMagic) + aead.NonceSize()
	if len(data) < header+aead.Overhead() || string(data[:len(trustMagic)]) != trustMagic {
		return nil, ErrInvalid
	}
	plain, err := aead.Open(nil, data[len(trustMagic):header], data[header:], []byte(s.identity.ServerID+"\x00"+trustFile))
	if err != nil {
		return nil, ErrInvalid
	}
	return plain, nil
}

func (s *Store) SaveTrust(data []byte) error {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	if len(data) == 0 || len(data) > maxTrustBytes {
		return ErrInvalid
	}
	key, err := s.trustKey(true)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return ErrIO
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return ErrIO
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return ErrIO
	}
	encrypted := append([]byte(trustMagic), nonce...)
	encrypted = aead.Seal(encrypted, nonce, data, []byte(s.identity.ServerID+"\x00"+trustFile))
	return s.writeAtomic(trustFile, encrypted)
}

func (s *Store) trustKey(create bool) ([]byte, error) {
	key, err := s.readPrivate(masterFile, 32)
	if err == nil {
		if len(key) != 32 {
			return nil, ErrInvalid
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !create {
		return nil, ErrInvalid
	}
	// Never regenerate a missing key for an already existing encrypted store.
	if _, err = s.root.Lstat(trustFile); !errors.Is(err, os.ErrNotExist) {
		return nil, ErrInvalid
	}
	key = make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, ErrIO
	}
	if err = s.writeAtomic(masterFile, key); err != nil {
		return nil, err
	}
	return key, nil
}

func (s *Store) readPrivate(name string, limit int) ([]byte, error) {
	if err := privateFile(s.root, name, false); err != nil {
		if _, e := s.root.Lstat(name); errors.Is(e, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	f, err := s.root.Open(name)
	if err != nil {
		return nil, ErrIO
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, ErrIO
	}
	if len(b) == 0 || len(b) > limit {
		return nil, ErrInvalid
	}
	return b, nil
}

func (s *Store) writeAtomic(name string, data []byte) error {
	if err := privateFile(s.root, name, true); err != nil {
		return err
	}
	temp := ".write-" + rand.Text()
	f, err := s.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrIO
	}
	defer s.root.Remove(temp)
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return ErrIO
	}
	if err = s.root.Rename(temp, name); err != nil {
		return ErrIO
	}
	dir, err := s.root.Open(".")
	if err != nil {
		return ErrIO
	}
	syncErr = dir.Sync()
	closeErr = dir.Close()
	if syncErr != nil || closeErr != nil {
		return ErrIO
	}
	return nil
}
