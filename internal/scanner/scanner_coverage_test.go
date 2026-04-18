package scanner

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kirskov/Shapin/internal/contract"
	"github.com/Kirskov/Shapin/internal/providers"
)

const (
	pinnedCheckoutLine = "      - uses: actions/checkout@" + testFakeSHA + " # v4\n"
	runErrFmt          = "Run: %v"
)

// ── assertWithinRoot ──────────────────────────────────────────────────────────

func TestAssertWithinRoot(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub", "file.yml")
	if err := assertWithinRoot(sub, dir); err != nil {
		t.Errorf("expected no error for path inside root, got: %v", err)
	}
	outside := filepath.Join(dir, "..", "other")
	if err := assertWithinRoot(outside, dir); err == nil {
		t.Error("expected error for path outside root")
	}
}

// ── nothingToDoMessage ────────────────────────────────────────────────────────

func TestNothingToDoMessage(t *testing.T) {
	cases := []struct {
		pinActions, pinImages bool
		want                  string
	}{
		{false, false, "both --pin-refs and --pin-images are disabled"},
		{true, false, "--pin-images=false"},
		{false, true, "--pin-refs=false"},
		{true, true, "Everything already pinned"},
	}
	for _, c := range cases {
		msg := nothingToDoMessage(c.pinActions, c.pinImages)
		if !strings.Contains(msg, c.want) {
			t.Errorf("nothingToDoMessage(%v,%v) = %q, want substring %q", c.pinActions, c.pinImages, msg, c.want)
		}
	}
}

// ── openOutput ────────────────────────────────────────────────────────────────

func TestOpenOutputStdout(t *testing.T) {
	w, close, err := openOutput("")
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	if w != os.Stdout {
		t.Error("expected os.Stdout when path is empty")
	}
}

func TestOpenOutputFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	w, close, err := openOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	close()
	data, _ := os.ReadFile(path)
	if string(data) != "hello" {
		t.Errorf("expected 'hello', got %q", string(data))
	}
}

// ── matchProvider ─────────────────────────────────────────────────────────────

func TestMatchProvider(t *testing.T) {
	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := ghDir + ciYML
	writeFile(t, path, "name: CI\n")

	pl := []contract.Provider{providers.NewGitHubResolver("")}
	p, err := matchProvider(path, dir, pl)
	if err != nil {
		t.Fatalf("expected match, got: %v", err)
	}
	if p == nil {
		t.Error("expected non-nil provider")
	}
}

func TestMatchProviderNoMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unknown.txt")
	writeFile(t, path, "content\n")

	pl := []contract.Provider{providers.NewGitHubResolver("")}
	_, err := matchProvider(path, dir, pl)
	if err == nil {
		t.Error("expected error for unmatched file")
	}
}

// ── renderJSON ────────────────────────────────────────────────────────────────

func TestRenderJSON(t *testing.T) {
	changes := []FileChange{
		{Path: "foo.yml", Changes: []Hunk{{Old: "old", New: "new", Line: 1}}},
	}
	var buf bytes.Buffer
	if err := renderJSON(&buf, changes); err != nil {
		t.Fatal(err)
	}
	var out []FileChange
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(out) != 1 || out[0].Path != "foo.yml" {
		t.Errorf("unexpected output: %v", out)
	}
}

func TestRenderJSONEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderJSON(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "null" {
		t.Errorf("expected null for empty changes, got %q", buf.String())
	}
}

// ── renderSARIF ───────────────────────────────────────────────────────────────

func TestRenderSARIF(t *testing.T) {
	changes := []FileChange{
		{Path: "foo.yml", Changes: []Hunk{{Old: "old", New: "new", Line: 5}}},
	}
	var buf bytes.Buffer
	if err := renderSARIF(&buf, changes, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid SARIF JSON: %v", err)
	}
	if out["version"] != "2.1.0" {
		t.Errorf("expected SARIF version 2.1.0, got %v", out["version"])
	}
}

func TestRenderSARIFEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderSARIF(&buf, nil, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "2.1.0") {
		t.Error("expected SARIF version in output")
	}
}

// ── config_file ───────────────────────────────────────────────────────────────

func TestLoadConfigFileNotExist(t *testing.T) {
	cfg, err := LoadConfigFile("/nonexistent/path/.shapin.json")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config for missing file")
	}
}

func TestLoadConfigFileValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".shapin.json")
	writeFile(t, path, `{"dry-run": false, "pin-refs": true, "pin-images": false}`)

	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if *cfg.DryRun != false {
		t.Error("expected dry-run=false")
	}
}

func TestLoadConfigFileInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".shapin.json")
	writeFile(t, path, `{invalid json}`)

	_, err := LoadConfigFile(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestConfigFileApplyTo(t *testing.T) {
	f := true
	host := "https://gitlab.example.com"
	token := "mytoken"
	exclude := []string{"*.skip.yml"}
	mappings := map[string]string{"MYAPP": "registry/myapp"}

	cf := &ConfigFile{
		DryRun:      &f,
		GitLabHost:  &host,
		GitLabToken: &token,
		Exclude:     exclude,
		TagMappings: mappings,
	}

	cfg := Config{}
	cf.ApplyTo(&cfg, map[string]bool{})

	if !cfg.DryRun {
		t.Error("expected DryRun=true")
	}
	if cfg.GitLabHost != host {
		t.Errorf("expected GitLabHost=%q, got %q", host, cfg.GitLabHost)
	}
	if cfg.GitLabToken != token {
		t.Errorf("expected GitLabToken=%q, got %q", token, cfg.GitLabToken)
	}
	if len(cfg.Exclude) != 1 {
		t.Errorf("expected 1 exclude pattern, got %d", len(cfg.Exclude))
	}
	if cfg.TagMappings["MYAPP"] != "registry/myapp" {
		t.Error("expected tag mapping to be applied")
	}
}

func TestConfigFileApplyToNil(t *testing.T) {
	var cf *ConfigFile
	cfg := Config{DryRun: true}
	cf.ApplyTo(&cfg, map[string]bool{})
	if !cfg.DryRun {
		t.Error("nil ConfigFile.ApplyTo should be a no-op")
	}
}

func TestConfigFileApplyToExplicitFlags(t *testing.T) {
	f := false
	cf := &ConfigFile{DryRun: &f}
	cfg := Config{DryRun: true}
	cf.ApplyTo(&cfg, map[string]bool{"dry-run": true})
	if !cfg.DryRun {
		t.Error("explicit CLI flag should take precedence over config file")
	}
}

// ── Run ───────────────────────────────────────────────────────────────────────

func TestRunDryRunNoFiles(t *testing.T) {
	dir := t.TempDir()
	err := Run(Config{
		Path:       dir,
		DryRun:     true,
		PinActions: true,
		PinImages:  true,
		Format:     FormatText,
	})
	if err != nil {
		t.Fatalf(runErrFmt, err)
	}
}

func TestRunNothingToDoAlreadyPinned(t *testing.T) {
	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Already pinned — Run should print "nothing to do"
	writeFile(t, ghDir+ciYML, pinnedCheckoutLine)
	err := Run(Config{
		Path:       dir,
		DryRun:     true,
		PinActions: true,
		PinImages:  true,
		Format:     FormatText,
	})
	if err != nil {
		t.Fatalf(runErrFmt, err)
	}
}

func TestRunNothingToDoPinActionsDisabled(t *testing.T) {
	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ghDir+ciYML, pinnedCheckoutLine)
	err := Run(Config{
		Path:       dir,
		DryRun:     true,
		PinActions: false,
		PinImages:  false,
		Format:     FormatText,
	})
	if err != nil {
		t.Fatalf(runErrFmt, err)
	}
}

func TestRunOutputToFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.txt")
	err := Run(Config{
		Path:       dir,
		DryRun:     true,
		PinActions: true,
		PinImages:  true,
		Format:     FormatText,
		Output:     outPath,
	})
	if err != nil {
		t.Fatalf(runErrFmt, err)
	}
}

func TestRunJSONFormat(t *testing.T) {
	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ghDir+ciYML, pinnedCheckoutLine)
	outPath := filepath.Join(dir, "out.json")
	err := Run(Config{
		Path:       dir,
		DryRun:     true,
		PinActions: true,
		PinImages:  true,
		Format:     FormatJSON,
		Output:     outPath,
	})
	if err != nil {
		t.Fatalf("Run JSON: %v", err)
	}
}

func TestRunSARIFFormat(t *testing.T) {
	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ghDir+ciYML, pinnedCheckoutLine)
	outPath := filepath.Join(dir, "out.sarif")
	err := Run(Config{
		Path:       dir,
		DryRun:     true,
		PinActions: true,
		PinImages:  true,
		Format:     FormatSARIF,
		Output:     outPath,
	})
	if err != nil {
		t.Fatalf("Run SARIF: %v", err)
	}
}

func TestRunMatchesGlobInvalidPattern(t *testing.T) {
	// matchesGlob with invalid pattern should return false without panic
	result := matchesGlob("[invalid", "file.yml")
	if result {
		t.Error("expected false for invalid glob pattern")
	}
}

func TestRunAppliesChanges(t *testing.T) {
	srv := newFakeGitHubServer(map[string]string{"v4": testFakeSHA})
	defer srv.Close()

	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ghDir+ciYML, checkoutV4Line)

	// Inject fake HTTP client via a custom provider list is not possible through Run directly,
	// so we test Run with a real path and no token (will fail to resolve, but exercises the path).
	// Use processFile directly with fake server instead.
	pl := []contract.Provider{
		providers.NewGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)}),
	}
	fc, _, err := processFile(ghDir+ciYML, dir, pl, processOpts{
		dryRun:     false,
		pinActions: true,
		pinImages:  false,
		format:     FormatText,
		out:        os.Stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fc == nil {
		t.Error("expected file to change")
	}
}

func TestRunJSON(t *testing.T) {
	srv := newFakeGitHubServer(map[string]string{"v4": testFakeSHA})
	defer srv.Close()

	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ghDir+ciYML, checkoutV4Line)

	outFile := filepath.Join(dir, "out.json")
	pl := []contract.Provider{
		providers.NewGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)}),
	}
	fc, _, err := processFile(ghDir+ciYML, dir, pl, processOpts{
		dryRun:     true,
		pinActions: true,
		pinImages:  false,
		format:     FormatJSON,
		out:        os.Stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = outFile
	_ = fc
}

func TestRunSARIF(t *testing.T) {
	srv := newFakeGitHubServer(map[string]string{"v4": testFakeSHA})
	defer srv.Close()

	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ghDir+ciYML, checkoutV4Line)

	pl := []contract.Provider{
		providers.NewGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)}),
	}
	fc, _, err := processFile(ghDir+ciYML, dir, pl, processOpts{
		dryRun:     true,
		pinActions: true,
		pinImages:  false,
		format:     FormatSARIF,
		out:        os.Stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = fc
}
