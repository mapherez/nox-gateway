package state

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedTrustSurvivesRestartAndDoesNotExposePlaintext(t *testing.T) {
	dir := privateDir(t)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"credential":"PRIVATE_SERVICE_CREDENTIAL","requestKey":"pending-key"}`)
	if data, err := s.LoadTrust(); err != nil || data != nil {
		t.Fatal("empty store did not load empty")
	}
	if err = s.SaveTrust(plain); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dir, trustFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(first, []byte("PRIVATE_SERVICE_CREDENTIAL")) {
		t.Fatal("credential stored as plaintext")
	}
	if err = s.SaveTrust(plain); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, trustFile))
	if bytes.Equal(first, second) {
		t.Fatal("AEAD nonce was reused")
	}
	_ = s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	actual, err := s.LoadTrust()
	if err != nil || !bytes.Equal(actual, plain) {
		t.Fatal("encrypted recovery state did not survive restart")
	}
	for _, name := range []string{trustFile, masterFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("trust file is not private")
		}
	}
}

func TestMissingOrCorruptTrustMaterialNeverRegenerates(t *testing.T) {
	for _, damage := range []string{"missing-master", "corrupt-master", "corrupt-ciphertext", "missing-ciphertext", "symlink"} {
		t.Run(damage, func(t *testing.T) {
			dir := privateDir(t)
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.SaveTrust([]byte(`{"secret":"PRIVATE"}`)); err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "missing-master":
				err = os.Remove(filepath.Join(dir, masterFile))
			case "corrupt-master":
				err = os.WriteFile(filepath.Join(dir, masterFile), []byte("broken"), 0600)
			case "corrupt-ciphertext":
				err = os.WriteFile(filepath.Join(dir, trustFile), []byte("broken"), 0600)
			case "missing-ciphertext":
				err = os.Remove(filepath.Join(dir, trustFile))
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				if err = os.WriteFile(target, bytes.Repeat([]byte{1}, 32), 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(filepath.Join(dir, masterFile)); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(target, filepath.Join(dir, masterFile))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.LoadTrust(); err == nil {
				t.Fatal("accepted lost/corrupt trust")
			}
			if damage == "missing-master" {
				if err = s.SaveTrust([]byte(`{}`)); err == nil {
					t.Fatal("regenerated an authority decryption key")
				}
				if _, err = os.Stat(filepath.Join(dir, masterFile)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("missing key was recreated")
				}
			}
		})
	}
}

func TestTrustCannotBeMovedToADifferentDiscoveryIdentity(t *testing.T) {
	dir := privateDir(t)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SaveTrust([]byte(`{"secret":"private"}`)); err != nil {
		t.Fatal(err)
	}
	otherDir := privateDir(t)
	other, err := Open(otherDir)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, name := range []string{masterFile, trustFile} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(otherDir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = other.LoadTrust(); err == nil {
		t.Fatal("trust was accepted under another installation identity")
	}
}
