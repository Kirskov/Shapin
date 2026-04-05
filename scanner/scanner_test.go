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

	"pintosha/provider"
)

const (
	ghWorkflowCI       = ".github/workflows/ci.yml"
	ghWorkflowSkip     = ".github/workflows/skip.yml"
	ghWorkflowDir      = "/.github/workflows"
	ciYML              = "/ci.yml"
	checkoutV4Line     = "      - uses: actions/checkout@v4\n"
	testFakeSHA        = "aabbccdd11223344556677889900aabbccdd1100"
	gitlabCom          = "https://gitlab.com"
	gitlabCIYML        = ".gitlab-ci.yml"
	manifestsPath      = "/manifests/"
	dockerDigestHeader = "Docker-Content-Digest"
	wantDigestInOutput = "expected digest in output, got:\n%s"
	wantTagAsComment   = "expected original tag as comment, got:\n%s"
	wantSHAInOutput    = "expected SHA in output, got:\n%s"
	gitRefsTagsPath    = "/git/refs/tags/"
	commitsPath        = "/commits/"
	trivyVersion        = "0.69.3"
	wantTrivyTagComment = "# " + trivyVersion
)

// ── isSHA ────────────────────────────────────────────────────────────────────

func TestIsSHA(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true}, // 40 hex chars
		{"0000000000000000000000000000000000000000", true}, // all zeros
		{"sha256:abc123", true},                            // docker digest
		{"v4", false},                                      // tag
		{"main", false},                                    // branch
		{"abc123", false},                                  // short sha
		{"", false},
	}
	for _, c := range cases {
		if got := isSHA(c.input); got != c.want {
			t.Errorf("isSHA(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

// ── isGitHubWorkflow ─────────────────────────────────────────────────────────

func TestIsGitHubWorkflow(t *testing.T) {
	p := newGitHubResolver("")
	cases := []struct {
		path string
		want bool
	}{
		{ghWorkflowCI, true},
		{".github/workflows/release.yaml", true},
		{".github/workflows/sub/deploy.yml", true},
		{".github/ci.yml", false},
		{"workflows/ci.yml", false},
		{gitlabCIYML, false},
		{"src/main.go", false},
	}
	for _, c := range cases {
		if got := p.IsMatch(c.path); got != c.want {
			t.Errorf("GitHub IsMatch(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// ── isGitLabCI ───────────────────────────────────────────────────────────────

func TestIsGitLabCI(t *testing.T) {
	p := newGitLabResolver(gitlabCom, "")
	cases := []struct {
		path string
		want bool
	}{
		{gitlabCIYML, true},
		{".gitlab-ci.yaml", true},
		{".gitlab-ci-build.yml", true},
		{".gitlab/ci.yml", true},
		{".gitlab/templates/deploy.yaml", true},
		{ghWorkflowCI, false},
		{"src/gitlab.yml", false},
		{"ci.yml", false},
	}
	for _, c := range cases {
		if got := p.IsMatch(c.path); got != c.want {
			t.Errorf("GitLab IsMatch(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// ── CircleCI ──────────────────────────────────────────────────────────────────

func TestIsCircleCI(t *testing.T) {
	p := newCircleCIResolver("")
	cases := []struct {
		path string
		want bool
	}{
		{circleciConfigYML, true},
		{".circleci/config.yaml", true},
		{".circleci/other.yml", false},
		{ghWorkflowCI, false},
		{"config.yml", false},
	}
	for _, c := range cases {
		if got := p.IsMatch(c.path); got != c.want {
			t.Errorf("CircleCI IsMatch(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestCircleCIPinsImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:circleci01")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := newCircleCIResolver("")
	p.setClient(&http.Client{Transport: rewriteHost(srv.URL)})

	content := "      image: myregistry.example.com/myimage:1.0.0\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:circleci01") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# 1.0.0") {
		t.Errorf(wantTagAsComment, got)
	}
}

func TestCircleCISkipsWhenPinImagesFalse(t *testing.T) {
	p := newCircleCIResolver("")
	content := "      image: myregistry.example.com/myimage:1.0.0\n"
	got, err := p.Resolve(content, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("expected content unchanged when pinImages=false, got:\n%s", got)
	}
}

// ── Forgejo Actions ───────────────────────────────────────────────────────────

func TestIsForgejoActions(t *testing.T) {
	p := newForgejoResolver("", "")
	cases := []struct {
		path string
		want bool
	}{
		{".forgejo/workflows/ci.yml", true},
		{".forgejo/workflows/release.yaml", true},
		{".forgejo/workflows/sub/deploy.yml", true},
		{".forgejo/ci.yml", false},
		{ghWorkflowCI, false},
		{gitlabCIYML, false},
	}
	for _, c := range cases {
		if got := p.IsMatch(c.path); got != c.want {
			t.Errorf("Forgejo IsMatch(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func newFakeForgejoServer(tagSHAs map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GET /api/v1/repos/owner/repo/git/refs/tags/v1
		if strings.Contains(r.URL.Path, gitRefsTagsPath) {
			parts := strings.Split(r.URL.Path, gitRefsTagsPath)
			tag := parts[1]
			sha, ok := tagSHAs[tag]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode([]map[string]any{
				{"object": map[string]string{"sha": sha}},
			})
			return
		}
		// GET /api/v1/repos/owner/repo/commits/ref (branch fallback)
		if strings.Contains(r.URL.Path, commitsPath) {
			parts := strings.Split(r.URL.Path, commitsPath)
			ref := parts[1]
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

func TestForgejoResolverPinsActions(t *testing.T) {
	srv := newFakeForgejoServer(map[string]string{"v1": testFakeSHA})
	defer srv.Close()

	r := newForgejoResolver(srv.URL, "")
	content := "      - uses: actions/checkout@v1\n"
	got, err := r.Resolve(content, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, testFakeSHA) {
		t.Errorf(wantSHAInOutput, got)
	}
	if !strings.Contains(got, "# v1") {
		t.Errorf(wantTagAsComment, got)
	}
}

func TestForgejoResolverSkipsAlreadyPinned(t *testing.T) {
	srv := newFakeForgejoServer(map[string]string{"v1": testFakeSHA})
	defer srv.Close()

	r := newForgejoResolver(srv.URL, "")
	content := fmt.Sprintf("      - uses: actions/checkout@%s # v1\n", testFakeSHA)
	got, err := r.Resolve(content, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("expected content unchanged, got:\n%s", got)
	}
}

// ── Bitbucket Pipelines ───────────────────────────────────────────────────────

func TestIsBitbucketPipelines(t *testing.T) {
	p := newBitbucketResolver()
	cases := []struct {
		path string
		want bool
	}{
		{"bitbucket-pipelines.yml", true},
		{"bitbucket-pipelines.yaml", true},
		{"bitbucket-pipelines-other.yml", false},
		{ghWorkflowCI, false},
		{gitlabCIYML, false},
	}
	for _, c := range cases {
		if got := p.IsMatch(c.path); got != c.want {
			t.Errorf("Bitbucket IsMatch(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestBitbucketPipelinesPinsImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:bitbucket01")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := newBitbucketResolver()
	p.setClient(&http.Client{Transport: rewriteHost(srv.URL)})

	content := "      image: myregistry.example.com/myimage:3.0.0\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:bitbucket01") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# 3.0.0") {
		t.Errorf(wantTagAsComment, got)
	}
}

func TestBitbucketPipelinesSkipsWhenPinImagesFalse(t *testing.T) {
	p := newBitbucketResolver()
	content := "      image: myregistry.example.com/myimage:3.0.0\n"
	got, err := p.Resolve(content, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("expected content unchanged when pinImages=false, got:\n%s", got)
	}
}

// ── splitRegistryAndRepo ─────────────────────────────────────────────────────

func TestSplitRegistryAndRepo(t *testing.T) {
	cases := []struct {
		image    string
		wantHost string
		wantRepo string
	}{
		{"maildev/maildev", "registry-1.docker.io", "maildev/maildev"},
		{"nginx", "registry-1.docker.io", "library/nginx"},
		{"registry.gitlab.com/group/image", "registry.gitlab.com", "group/image"},
		{"ghcr.io/owner/repo", "ghcr.io", "owner/repo"},
		{"quay.io/prometheus/golang-builder", "quay.io", "prometheus/golang-builder"},
	}
	for _, c := range cases {
		host, repo := splitRegistryAndRepo(c.image)
		if host != c.wantHost || repo != c.wantRepo {
			t.Errorf("splitRegistryAndRepo(%q) = (%q, %q), want (%q, %q)",
				c.image, host, repo, c.wantHost, c.wantRepo)
		}
	}
}

// ── extractProjectPath ───────────────────────────────────────────────────────

func TestExtractProjectPath(t *testing.T) {
	const groupProject = "group/project"
	cases := []struct {
		component string
		want      string
	}{
		{"gitlab.com/group/project/component", groupProject},
		{"group/project/component", groupProject},
		{"gitlab.com/group/project", groupProject},
		{"group/project", groupProject},
		{"only", ""},
	}
	for _, c := range cases {
		if got := extractProjectPath(c.component); got != c.want {
			t.Errorf("extractProjectPath(%q) = %q, want %q", c.component, got, c.want)
		}
	}
}

// ── githubResolver.resolve ───────────────────────────────────────────────────

func newFakeGitHubServer(tagSHAs map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GET /repos/owner/repo/git/refs/tags/v1
		if strings.Contains(r.URL.Path, gitRefsTagsPath) {
			parts := strings.Split(r.URL.Path, gitRefsTagsPath)
			tag := parts[1]
			sha, ok := tagSHAs[tag]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{"sha": sha, "type": "commit"},
			})
			return
		}
		// GET /repos/owner/repo/commits/ref (branch fallback)
		if strings.Contains(r.URL.Path, commitsPath) {
			parts := strings.Split(r.URL.Path, commitsPath)
			ref := parts[1]
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

func TestGitHubResolverPinsActions(t *testing.T) {
	fakeSHA := testFakeSHA
	srv := newFakeGitHubServer(map[string]string{"v4": fakeSHA})
	defer srv.Close()

	r := newGitHubResolver("")
	// Point the resolver at our fake server
	r.client = &http.Client{
		Transport: rewriteHost(srv.URL),
	}

	content := checkoutV4Line
	got, err := r.Resolve(content, true, false)
	if err != nil {
		t.Fatal(err)
	}

	wantContains := fmt.Sprintf("actions/checkout@%s # v4", fakeSHA)
	if !strings.Contains(got, wantContains) {
		t.Errorf("expected %q in output, got:\n%s", wantContains, got)
	}
}

func TestGitHubResolverSkipsAlreadyPinned(t *testing.T) {
	// Fake server returns the same SHA — no drift, content must be unchanged.
	srv := newFakeGitHubServer(map[string]string{"v4": testFakeSHA})
	defer srv.Close()

	r := newGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)})
	content := fmt.Sprintf("      - uses: actions/checkout@%s # v4\n", testFakeSHA)

	got, err := r.Resolve(content, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("expected content to be unchanged, got:\n%s", got)
	}
}

func TestGitHubResolverPinImages(t *testing.T) {
	// Docker resolver needs a fake registry — skip API calls by providing a
	// server that returns a digest header.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:deadbeef")
			w.WriteHeader(http.StatusOK)
			return
		}
		// Auth token endpoint
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	r := newGitHubResolver("")
	r.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := "        image: myregistry.example.com/myimage:1.2.3\n"
	got, err := r.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:deadbeef") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# 1.2.3") {
		t.Errorf(wantTagAsComment, got)
	}
}

// ── dockerResolver ───────────────────────────────────────────────────────────

func TestDockerResolverSkipsLatest(t *testing.T) {
	d := newDockerResolver("")
	content := "image: nginx:latest\n"
	got := d.resolveImages(content)
	if got != content {
		t.Errorf("expected 'latest' to be skipped, got:\n%s", got)
	}
}

func TestDockerResolverSkipsDigest(t *testing.T) {
	d := newDockerResolver("")
	content := "image: nginx@sha256:abc123\n"
	got := d.resolveImages(content)
	if got != content {
		t.Errorf("expected already-digested image to be skipped, got:\n%s", got)
	}
}

// ── runner ───────────────────────────────────────────────────────────────────

func TestFindWorkflowFiles(t *testing.T) {
	dir := t.TempDir()

	// Create matching files
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

	// Non-matching files
	writeFile(t, dir+"/README.md", "# readme\n")
	if err := os.MkdirAll(dir+"/node_modules/.bin", 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir+"/node_modules/.bin/ci.yml", "should be skipped\n")

	providers := []provider.Provider{
		newGitHubResolver(""),
		newGitLabResolver(gitlabCom, ""),
	}

	files, err := findWorkflowFiles(dir, providers, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 4 {
		t.Errorf("expected 4 files, got %d: %v", len(files), files)
	}

	// node_modules must not appear
	for _, f := range files {
		if strings.Contains(f, "node_modules") {
			t.Errorf("node_modules file should be excluded: %s", f)
		}
	}
}

func TestRunnerDryRun(t *testing.T) {
	fakeSHA := testFakeSHA
	srv := newFakeGitHubServer(map[string]string{"v4": fakeSHA})
	defer srv.Close()

	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}

	original := checkoutV4Line
	writeFile(t, ghDir+ciYML, original)

	// We test the dry-run contract: file content must not change
	providers := []provider.Provider{
		newGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)}),
		newGitLabResolver(gitlabCom, ""),
	}

	files, err := findWorkflowFiles(dir, providers, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range files {
		_, err := processFile(f, dir, providers, processOpts{dryRun: true, pinActions: true, pinImages: false, format: FormatText, out: os.Stdout})
		if err != nil {
			t.Fatalf("processFile: %v", err)
		}
	}

	// File must be unchanged in dry-run mode
	got, err := os.ReadFile(ghDir + ciYML)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("dry-run modified file; want original content, got:\n%s", string(got))
	}
}

func TestRunnerAppliesChanges(t *testing.T) {
	fakeSHA := testFakeSHA
	srv := newFakeGitHubServer(map[string]string{"v4": fakeSHA})
	defer srv.Close()

	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ghDir+ciYML, checkoutV4Line)

	providers := []provider.Provider{
		newGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)}),
		newGitLabResolver(gitlabCom, ""),
	}

	fc, err := processFile(ghDir+ciYML, dir, providers, processOpts{dryRun: false, pinActions: true, pinImages: false, format: FormatText, out: os.Stdout})
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
	if !strings.Contains(string(got), fakeSHA) {
		t.Errorf("expected pinned SHA in file, got:\n%s", string(got))
	}
}

func TestRunnerConcurrency(t *testing.T) {
	// Create many files and verify all are processed without data races.
	// Run with: go test -race ./scanner/...
	srv := newFakeGitHubServer(map[string]string{"v3": testFakeSHA, "v4": testFakeSHA})
	defer srv.Close()

	dir := t.TempDir()
	ghDir := dir + ghWorkflowDir
	if err := os.MkdirAll(ghDir, 0755); err != nil {
		t.Fatal(err)
	}

	const numFiles = 20
	createCheckoutFiles(t, ghDir, numFiles)

	providers := []provider.Provider{
		newGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)}),
		newGitLabResolver(gitlabCom, ""),
	}

	files, err := findWorkflowFiles(dir, providers, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != numFiles {
		t.Fatalf("expected %d files, got %d", numFiles, len(files))
	}

	anyChanged := processFilesConcurrently(t, files, dir, providers)
	if !anyChanged {
		t.Error("expected at least one file to change")
	}

	assertAllFilesPinned(t, ghDir, numFiles, testFakeSHA)
}

func createCheckoutFiles(t *testing.T, ghDir string, n int) {
	t.Helper()
	for i := range n {
		ref := "v4"
		if i%2 == 0 {
			ref = "v3"
		}
		name := fmt.Sprintf("%s/workflow_%d.yml", ghDir, i)
		writeFile(t, name, fmt.Sprintf("      - uses: actions/checkout@%s\n", ref))
	}
}

func processFilesConcurrently(t *testing.T, files []string, root string, providers []provider.Provider) bool {
	t.Helper()
	var (
		wg         sync.WaitGroup
		sem        = make(chan struct{}, 8)
		anyChanged atomic.Bool
	)
	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()
			fc, err := processFile(path, root, providers, processOpts{dryRun: false, pinActions: true, pinImages: false, format: FormatText, out: os.Stdout})
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
	return anyChanged.Load()
}

func assertAllFilesPinned(t *testing.T, ghDir string, n int, sha string) {
	t.Helper()
	for i := range n {
		name := fmt.Sprintf("%s/workflow_%d.yml", ghDir, i)
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), sha) {
			t.Errorf("file %d was not pinned: %s", i, string(data))
		}
	}
}

// ── GitLab resolver ───────────────────────────────────────────────────────────

func newFakeGitLabServer(commitSHAs map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GET /api/v4/projects/:id/repository/commits/:ref
		for ref, sha := range commitSHAs {
			if strings.HasSuffix(r.URL.Path, commitsPath+ref) {
				json.NewEncoder(w).Encode(map[string]string{"id": sha})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestGitLabResolverPinsComponents(t *testing.T) {
	fakeSHA := testFakeSHA
	srv := newFakeGitLabServer(map[string]string{"v1.0": fakeSHA})
	defer srv.Close()

	r := newGitLabResolver(srv.URL, "")
	content := "  - component: gitlab.com/mygroup/myproject/mycomp@v1.0\n"
	got, err := r.Resolve(content, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, fakeSHA) {
		t.Errorf(wantSHAInOutput, got)
	}
	if !strings.Contains(got, "# v1.0") {
		t.Errorf("expected original ref as comment, got:\n%s", got)
	}
}

func TestGitLabResolverSkipsAlreadyPinned(t *testing.T) {
	r := newGitLabResolver(gitlabCom, "")
	content := fmt.Sprintf("  - component: gitlab.com/g/p/c@%s\n", testFakeSHA)
	got, err := r.Resolve(content, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("expected content unchanged, got:\n%s", got)
	}
}

func TestGitLabResolverPinsImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:gitlab01")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	r := newGitLabResolver(gitlabCom, "")
	r.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := "  image: myregistry.example.com/myimage:2.0.0\n"
	got, err := r.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:gitlab01") {
		t.Errorf(wantDigestInOutput, got)
	}
}

func TestGitLabResolverPinsInputTAGKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:trivydigest01")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	r := newGitLabResolver(gitlabCom, "")
	r.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := `include:
  - component: gitlab.com/group/project/trivy@v1.0
    inputs:
      TRIVY_TAG: myregistry.example.com/trivy:0.69.3
      severity: HIGH
`
	got, err := r.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:trivydigest01") {
		t.Errorf("expected digest in TRIVY_TAG value, got:\n%s", got)
	}
	if !strings.Contains(got, wantTrivyTagComment) {
		t.Errorf(wantTagAsComment, got)
	}
	// Non-TAG key must be untouched
	if !strings.Contains(got, "severity: HIGH") {
		t.Errorf("expected severity to be unchanged, got:\n%s", got)
	}
}

func TestGitLabResolverSkipsNonTAGInputKeys(t *testing.T) {
	// Use a key name that has no "TAG" — resolveComponentInputs must skip it.
	// The value uses a key name "image" which is not a TAG key.
	// No network calls should be made for input pinning, but resolveImages
	// would normally try — use a fake server that returns 404 to keep it offline.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := newGitLabResolver(gitlabCom, "")
	r.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := `include:
  - component: gitlab.com/group/project/trivy@v1.0
    inputs:
      IMAGE_NAME: myregistry.example.com/trivy:0.69.3
      version: 1.2.3
`
	got, err := r.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	// IMAGE_NAME contains no TAG — nothing should be pinned via input logic
	if strings.Contains(got, "sha256:") {
		t.Errorf("expected no digest in non-TAG inputs, got:\n%s", got)
	}
}

func TestGitLabResolverPinsJobVariablesTAGKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:jobvardigest01")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	r := newGitLabResolver(gitlabCom, "")
	r.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := `scan:
  image: alpine:3.18
  variables:
    TRIVY_TAG: aquasec/trivy:0.69.3
  script:
    - echo hello
`
	got, err := r.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:jobvardigest01") {
		t.Errorf("expected digest in job-level TRIVY_TAG variable, got:\n%s", got)
	}
	if !strings.Contains(got, wantTrivyTagComment) {
		t.Errorf(wantTagAsComment, got)
	}
}

func TestGitLabResolverPinsVariablesTAGKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:vardigest01")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	r := newGitLabResolver(gitlabCom, "")
	r.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := `variables:
  TRIVY_TAG: myregistry.example.com/trivy:0.69.3
  APP_VERSION: "1.2.3"
`
	got, err := r.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:vardigest01") {
		t.Errorf("expected digest in TRIVY_TAG variable, got:\n%s", got)
	}
	if !strings.Contains(got, wantTrivyTagComment) {
		t.Errorf(wantTagAsComment, got)
	}
	// Non-TAG key must be untouched
	if !strings.Contains(got, `APP_VERSION: "1.2.3"`) {
		t.Errorf("expected APP_VERSION to be unchanged, got:\n%s", got)
	}
}

// ── GitHub branch fallback + annotated tag ────────────────────────────────────

func TestGitHubResolverFallsBackToBranch(t *testing.T) {
	fakeSHA := testFakeSHA
	// Tag endpoint returns 404, commits endpoint returns the SHA
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, gitRefsTagsPath) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(r.URL.Path, commitsPath) {
			json.NewEncoder(w).Encode(map[string]string{"sha": fakeSHA})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := newGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)})
	content := "      - uses: actions/checkout@main\n"
	got, err := r.Resolve(content, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, fakeSHA) {
		t.Errorf(wantSHAInOutput, got)
	}
}

func TestGitHubResolverAnnotatedTag(t *testing.T) {
	fakeSHA := testFakeSHA
	tagObjURL := "/repos/actions/checkout/git/tags/tagobj"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, gitRefsTagsPath) {
			// Returns a tag object (annotated tag)
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{
					"sha":  "tagobjectsha1234567890123456789012345678",
					"type": "tag",
					"url":  "https://api.github.com" + tagObjURL,
				},
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, tagObjURL) {
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{"sha": fakeSHA},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := newGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)})
	content := "      - uses: actions/checkout@v4\n"
	got, err := r.Resolve(content, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, fakeSHA) {
		t.Errorf("expected commit SHA in output, got:\n%s", got)
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

	providers := []provider.Provider{newGitHubResolver("")}

	files, err := findWorkflowFiles(dir, providers, []string{".github/workflows/release.yml"})
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

// ── doWithRetry ───────────────────────────────────────────────────────────────

func TestDoWithRetryOn429(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := doWithRetry(&http.Client{}, req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestDoWithRetryExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := doWithRetry(&http.Client{}, req)
	if err == nil {
		t.Error("expected error after exhausting retries")
	}
}

// ── config_file ───────────────────────────────────────────────────────────────

func TestLoadConfigFileNotExist(t *testing.T) {
	cfg, err := LoadConfigFile("/nonexistent/.digestify.json")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config for missing file")
	}
}

func TestLoadConfigFileValid(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	writeFile(t, path, `{"dry-run":false,"pin-actions":true,"exclude":["skip.yml"]}`)

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
	if *cfg.PinActions != true {
		t.Error("expected pin-actions=true")
	}
	if len(cfg.Exclude) != 1 || cfg.Exclude[0] != "skip.yml" {
		t.Errorf("unexpected exclude: %v", cfg.Exclude)
	}
}

func TestLoadConfigFileInvalid(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	writeFile(t, path, `{not valid json}`)

	_, err := LoadConfigFile(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestApplyToRespectsExplicitFlags(t *testing.T) {
	f := false
	cfgFile := &ConfigFile{DryRun: &f}

	cfg := Config{DryRun: true}
	// "dry-run" was explicitly set on CLI — config file must not override it
	cfgFile.ApplyTo(&cfg, map[string]bool{"dry-run": true})
	if !cfg.DryRun {
		t.Error("explicit CLI flag should not be overridden by config file")
	}
}

func TestApplyToFillsMissingFields(t *testing.T) {
	token := "mytoken"
	host := "https://gitlab.mycompany.com"
	cfgFile := &ConfigFile{GitLabToken: &token, GitLabHost: &host}

	cfg := Config{}
	cfgFile.ApplyTo(&cfg, map[string]bool{})
	if cfg.GitLabToken != token {
		t.Errorf("expected token %q, got %q", token, cfg.GitLabToken)
	}
	if cfg.GitLabHost != host {
		t.Errorf("expected host %q, got %q", host, cfg.GitLabHost)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// rewriteHost returns a RoundTripper that redirects all requests to the given base URL.
type rewriteHostTransport struct {
	base string
}

func rewriteHost(base string) http.RoundTripper {
	return &rewriteHostTransport{base: base}
}

func (t *rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace scheme+host with our fake server, keep path+query
	req2 := req.Clone(req.Context())
	req2.URL.Scheme = "http"
	req2.URL.Host = strings.TrimPrefix(t.base, "http://")
	return http.DefaultTransport.RoundTrip(req2)
}
