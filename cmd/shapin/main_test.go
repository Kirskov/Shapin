package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "shapin")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

func TestMainVersion(t *testing.T) {
	bin := buildBinary(t)
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("--version failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "dev") {
		t.Errorf("expected version output, got: %s", out)
	}
}

func TestMainDryRunNoFiles(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	out, err := exec.Command(bin, "--path", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, out)
	}
}

func TestMainInvalidConfigFile(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(cfgPath, []byte("{invalid}"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--config", cfgPath, "--path", dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("expected non-zero exit for invalid config, got output: %s", out)
	}
}
