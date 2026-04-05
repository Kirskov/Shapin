package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type rewriteHostTransport struct{ base string }

func (tr rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(tr.base, "http://")
	return http.DefaultTransport.RoundTrip(req)
}

func rewriteHost(base string) http.RoundTripper { return rewriteHostTransport{base: base} }

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
	p := NewGitHubResolver("")
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
	p := NewGitLabResolver(gitlabCom, "")
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
	p := NewCircleCIResolver("")
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

	p := NewCircleCIResolver("")
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
	p := NewCircleCIResolver("")
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
	p := NewForgejoResolver("", "")
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

	r := NewForgejoResolver(srv.URL, "")
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

	r := NewForgejoResolver(srv.URL, "")
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
	p := NewBitbucketResolver()
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

	p := NewBitbucketResolver()
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
	p := NewBitbucketResolver()
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

	r := NewGitHubResolver("")
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

	r := NewGitHubResolverWithClient("", &http.Client{Transport: rewriteHost(srv.URL)})
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

	r := NewGitHubResolver("")
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

// ── helpers ──────────────────────────────────────────────────────────────────

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
