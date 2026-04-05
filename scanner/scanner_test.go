package scanner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"pintosha/providers"
)

const (
	ghWorkflowCI   = ".github/workflows/ci.yml"
	ghWorkflowSkip = ".github/workflows/skip.yml"
	ghWorkflowDir  = "/.github/workflows"
	ciYML          = "/ci.yml"
	checkoutV4Line = "      - uses: actions/checkout@v4\n"
	testFakeSHA    = "aabbccdd11223344556677889900aabbccdd1100"
	gitlabCom      = "https://gitlab.com"
	gitlabCIYML    = ".gitlab-ci.yml"
	gitRefsTagsPath = "/git/refs/tags/"
	commitsPath     = "/commits/"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

type rewriteHostTransport struct{ base string }

func (tr rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(tr.base, "http://")
	return http.DefaultTransport.RoundTrip(req)
}

func rewriteHost(base string) http.RoundTripper { return rewriteHostTransport{base: base} }

func newFakeGitHubServer(tagSHAs map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, gitRefsTagsPath) {
			tag := strings.Split(r.URL.Path, gitRefsTagsPath)[1]
			sha, ok := tagSHAs[tag]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": sha, "type": "commit"}})
			return
		}
		if strings.Contains(r.URL.Path, commitsPath) {
			ref := strings.Split(r.URL.Path, commitsPath)[1]
			sha, ok := tagSHAs[ref]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"sha": sha})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

// ── findWorkflowFiles ─────────────────────────────────────────────────────────

func TestFindWorkflowFiles(t *testing.T) {
	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ghDir+ciYML, "name: CI\n")
	writeFile(t, ghDir+"/release.yaml", "name: Release\n")
	glDir := dir + "/.gitlab"
	if err := os.MkdirAll(glDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, glDir+"/pipeline.yml", "stage: build\n")
	writeFile(t, dir+"/.gitlab-ci.yml", "include:\n")
	writeFile(t, dir+"/README.md", "# readme\n")
	if err := os.MkdirAll(dir+"/node_modules/.bin", 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir+"/node_modules/.bin/ci.yml", "should be skipped\n")

	pl := []providers.Provider{
		providers.NewGitHubResolver(""),
		providers.NewGitLabResolver(gitlabCom, ""),
	}
	files, err := findWorkflowFiles(dir, pl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Errorf("expected 4 files, got %d: %v", len(files), files)
	}
	for _, f := range files {
		if strings.Contains(f, "node_modules") {
			t.Errorf("node_modules file should be excluded: %s", f)
		}
	}
}

// ── isExcluded ────────────────────────────────────────────────────────────────

func TestIsExcluded(t *testing.T) {
	cases := []struct {
		path     string
		patterns []string
		want     bool
	}{
		{ghWorkflowSkip, []string{ghWorkflowSkip}, true},
		{ghWorkflowCI, []string{"*.yml"}, true},
		{ghWorkflowCI, []string{ghWorkflowSkip}, false},
		{ghWorkflowCI, []string{}, false},
		{gitlabCIYML, []string{gitlabCIYML}, true},
	}
	for _, c := range cases {
		if got := isExcluded(c.path, c.patterns); got != c.want {
			t.Errorf("isExcluded(%q, %v) = %v, want %v", c.path, c.patterns, got, c.want)
		}
	}
}

func TestFindWorkflowFilesExclude(t *testing.T) {
	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ghDir+ciYML, checkoutV4Line)
	writeFile(t, ghDir+"/release.yml", checkoutV4Line)

	pl := []providers.Provider{providers.NewGitHubResolver("")}
	files, err := findWorkflowFiles(dir, pl, []string{".github/workflows/release.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file after exclude, got %d: %v", len(files), files)
	}
	if !strings.HasSuffix(files[0], ciYML) {
		t.Errorf("expected ci.yml to remain, got %s", files[0])
	}
}

// ── runner ────────────────────────────────────────────────────────────────────

func TestRunnerDryRun(t *testing.T) {
	srv := newFakeGitHubServer(map[string]string{"v4": testFakeSHA})
	defer srv.Close()

	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	original := checkoutV4Line
	writeFile(t, ghDir+ciYML, original)

	pl := []providers.Provider{
		providers.NewGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)}),
		providers.NewGitLabResolver(gitlabCom, ""),
	}
	files, err := findWorkflowFiles(dir, pl, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if _, err := processFile(f, dir, pl, processOpts{dryRun: true, pinActions: true, pinImages: false, format: FormatText, out: os.Stdout}); err != nil {
			t.Fatalf("processFile: %v", err)
		}
	}
	got, err := os.ReadFile(ghDir + ciYML)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("dry-run modified file; want original content, got:\n%s", string(got))
	}
}

func TestRunnerAppliesChanges(t *testing.T) {
	srv := newFakeGitHubServer(map[string]string{"v4": testFakeSHA})
	defer srv.Close()

	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ghDir+ciYML, checkoutV4Line)

	pl := []providers.Provider{
		providers.NewGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)}),
		providers.NewGitLabResolver(gitlabCom, ""),
	}
	fc, err := processFile(ghDir+ciYML, dir, pl, processOpts{dryRun: false, pinActions: true, pinImages: false, format: FormatText, out: os.Stdout})
	if err != nil {
		t.Fatalf("processFile: %v", err)
	}
	if fc == nil {
		t.Error("expected changed=true")
	}
	got, err := os.ReadFile(ghDir + ciYML)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), testFakeSHA) {
		t.Errorf("expected pinned SHA in file, got:\n%s", string(got))
	}
}

func TestRunnerConcurrency(t *testing.T) {
	srv := newFakeGitHubServer(map[string]string{"v3": testFakeSHA, "v4": testFakeSHA})
	defer srv.Close()

	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	const numFiles = 20
	for i := range numFiles {
		ref := "v4"
		if i%2 == 0 {
			ref = "v3"
		}
		writeFile(t, fmt.Sprintf("%s/workflow_%d.yml", ghDir, i),
			fmt.Sprintf("      - uses: actions/checkout@%s\n", ref))
	}

	pl := []providers.Provider{
		providers.NewGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)}),
		providers.NewGitLabResolver(gitlabCom, ""),
	}
	files, err := findWorkflowFiles(dir, pl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != numFiles {
		t.Fatalf("expected %d files, got %d", numFiles, len(files))
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var anyChanged atomic.Bool
	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()
			fc, err := processFile(path, dir, pl, processOpts{dryRun: false, pinActions: true, pinImages: false, format: FormatText, out: os.Stdout})
			if err != nil {
				t.Errorf("processFile(%s): %v", path, err)
				return
			}
			if fc != nil {
				anyChanged.Store(true)
			}
		}(f)
	}
	wg.Wait()
	if !anyChanged.Load() {
		t.Error("expected at least one file to change")
	}
	for i := range numFiles {
		data, err := os.ReadFile(fmt.Sprintf("%s/workflow_%d.yml", ghDir, i))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), testFakeSHA) {
			t.Errorf("file %d was not pinned: %s", i, string(data))
		}
	}
}
