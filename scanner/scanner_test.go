package scanner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── isSHA ────────────────────────────────────────────────────────────────────

func TestIsSHA(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},  // 40 hex chars
		{"0000000000000000000000000000000000000000", true},  // all zeros
		{"sha256:abc123", true},                              // docker digest
		{"v4", false},                                        // tag
		{"main", false},                                      // branch
		{"abc123", false},                                    // short sha
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
		{".github/workflows/ci.yml", true},
		{".github/workflows/release.yaml", true},
		{".github/workflows/sub/deploy.yml", true},
		{".github/ci.yml", false},
		{"workflows/ci.yml", false},
		{".gitlab-ci.yml", false},
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
	p := newGitLabResolver("https://gitlab.com", "")
	cases := []struct {
		path string
		want bool
	}{
		{".gitlab-ci.yml", true},
		{".gitlab-ci.yaml", true},
		{".gitlab-ci-build.yml", true},
		{".gitlab/ci.yml", true},
		{".gitlab/templates/deploy.yaml", true},
		{".github/workflows/ci.yml", false},
		{"src/gitlab.yml", false},
		{"ci.yml", false},
	}
	for _, c := range cases {
		if got := p.IsMatch(c.path); got != c.want {
			t.Errorf("GitLab IsMatch(%q) = %v, want %v", c.path, got, c.want)
		}
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
		if strings.Contains(r.URL.Path, "/git/refs/tags/") {
			parts := strings.Split(r.URL.Path, "/git/refs/tags/")
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
		if strings.Contains(r.URL.Path, "/commits/") {
			parts := strings.Split(r.URL.Path, "/commits/")
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
	fakeSHA := "aabbccdd11223344556677889900aabbccdd1100"
	srv := newFakeGitHubServer(map[string]string{"v4": fakeSHA})
	defer srv.Close()

	r := newGitHubResolver("")
	// Point the resolver at our fake server
	r.client = &http.Client{
		Transport: rewriteHost(srv.URL),
	}

	content := "      - uses: actions/checkout@v4\n"
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
	r := newGitHubResolver("")
	sha := "aabbccdd11223344556677889900aabbccdd1100"
	content := fmt.Sprintf("      - uses: actions/checkout@%s # v4\n", sha)

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
		if strings.Contains(r.URL.Path, "/manifests/") {
			w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
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
		t.Errorf("expected digest in output, got:\n%s", got)
	}
	if !strings.Contains(got, "# 1.2.3") {
		t.Errorf("expected original tag as comment, got:\n%s", got)
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

// ── helpers ──────────────────────────────────────────────────────────────────

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
