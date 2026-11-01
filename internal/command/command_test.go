package command

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInformationFlagsDoNotOpenStateOrRequireConfiguration(t *testing.T) {
	for _, arg := range []string{"--help", "--version"} {
		var out, logs bytes.Buffer
		lookup := func(string) (string, bool) { t.Fatal("information flag read configuration"); return "", false }
		if err := Run(t.Context(), []string{arg}, lookup, &out, &logs); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "nox-gateway") || logs.Len() != 0 {
			t.Fatal("unexpected information output")
		}
	}
}

func TestStartupErrorsNeverEchoUntrustedInputs(t *testing.T) {
	for _, args := range [][]string{{"--PRIVATE_KEY"}, {"PRIVATE_KEY"}} {
		var out, logs bytes.Buffer
		err := Run(t.Context(), args, func(string) (string, bool) { return "", false }, &out, &logs)
		if err == nil || strings.Contains(err.Error()+out.String()+logs.String(), "PRIVATE_KEY") {
			t.Fatal("unsafe argument error")
		}
	}
	var out, logs bytes.Buffer
	err := Run(t.Context(), nil, func(name string) (string, bool) {
		if name == "NOX_GATEWAY_LISTEN_ADDR" {
			return "https://PRIVATE_KEY@example.com", true
		}
		return "", false
	}, &out, &logs)
	if err == nil || strings.Contains(err.Error()+out.String()+logs.String(), "PRIVATE_KEY") {
		t.Fatal("unsafe configuration error")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte(`{"secret":"PRIVATE_KEY"}`), 0600); err != nil {
		t.Fatal(err)
	}
	err = Run(t.Context(), nil, func(name string) (string, bool) {
		if name == "NOX_GATEWAY_DATA_DIR" {
			return dir, true
		}
		return "", false
	}, &out, &logs)
	if err == nil || strings.Contains(err.Error()+out.String()+logs.String(), "PRIVATE_KEY") {
		t.Fatal("unsafe persistent state error")
	}
}

func TestCancelledCommandDoesNotCreateState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, logs bytes.Buffer
	err := Run(ctx, nil, func(name string) (string, bool) {
		if name == "NOX_GATEWAY_DATA_DIR" {
			return dir, true
		}
		return "", false
	}, &out, &logs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("cancelled command wrote persistent state")
	}
}
