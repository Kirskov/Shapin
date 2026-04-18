package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testGitLabCom       = defaultGitLabHost
	testOwnerRepo       = "owner/repo"
	testEncodedProject  = "group%2Fproject"
	testTagV1           = "v1.0.0"
	testGitRefsTagsPath  = "/git/refs/tags/"
	testExpectFmt        = "expected %q, got %q"
	testPinComponentsFmt = "pinComponents: %v"
)

// ── extractProjectPath ────────────────────────────────────────────────────────

func TestExtractProjectPath(t *testing.T) {
	cases := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"gitlab.com/group/project/component", "group/project", false},
		{"gitlab.com/group/project", "group/project", false},
		{"no-dot/group/project/name", "", true},   // no hostname
		{"gitlab.com/onlyone", "", true},           // too short
	}
	for _, c := range cases {
		got, err := extractProjectPath(c.input)
		if c.wantErr {
			if err == nil {
				t.Errorf("extractProjectPath(%q): expected error", c.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("extractProjectPath(%q): unexpected error: %v", c.input, err)
			continue
		}
		if got != c.want {
			t.Errorf("extractProjectPath(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// ── isUnstableBranch ──────────────────────────────────────────────────────────

func TestIsUnstableBranch(t *testing.T) {
	stable := []string{testTagV1, "1.2.3", "release-1.0"}
	for _, ref := range stable {
		if isUnstableBranch(ref) {
			t.Errorf("isUnstableBranch(%q) = true, want false", ref)
		}
	}
	unstable := []string{"main", "master", "develop", "feat/my-feature", "fix/bug", "hotfix/patch"}
	for _, ref := range unstable {
		if !isUnstableBranch(ref) {
			t.Errorf("isUnstableBranch(%q) = false, want true", ref)
		}
	}
}

// ── warnBranchRef / warnDrift ─────────────────────────────────────────────────

func TestWarnBranchRefDoesNotPanic(t *testing.T) {
	warnBranchRef("GitHub", "actions/checkout", "main")
}

func TestWarnDriftDoesNotPanic(t *testing.T) {
	warnDrift("tag", "actions/checkout", "v4", "oldsha", "newsha")
}

// ── Ansi / IsTTY ─────────────────────────────────────────────────────────────

func TestAnsiReturnEmptyInTest(t *testing.T) {
	// In test environment stdout is not a TTY — Ansi() should return "".
	got := Ansi(AnsiRed)
	if got != "" {
		t.Errorf("Ansi() in test should return empty string, got %q", got)
	}
}

// ── assertWithinRoot (providers) ──────────────────────────────────────────────

func TestAssertWithinRootProviders(t *testing.T) {
	t.TempDir() // ensure t.TempDir works
	dir := t.TempDir()
	sub := dir + "/sub/file.yml"
	if err := assertWithinRoot(sub, dir); err != nil {
		t.Errorf("expected no error for path inside root, got: %v", err)
	}
	outside := dir + "/../other"
	if err := assertWithinRoot(outside, dir); err == nil {
		t.Error("expected error for path outside root")
	}
}

// ── parseRetryAfter ───────────────────────────────────────────────────────────

func TestParseRetryAfterEmpty(t *testing.T) {
	_, ok := parseRetryAfter("")
	if ok {
		t.Error("expected ok=false for empty header")
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	d, ok := parseRetryAfter("30")
	if !ok {
		t.Error("expected ok=true for seconds value")
	}
	if d != 30*time.Second {
		t.Errorf("expected 30s, got %v", d)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(60 * time.Second).UTC().Format(http.TimeFormat)
	d, ok := parseRetryAfter(future)
	if !ok {
		t.Error("expected ok=true for HTTP date")
	}
	if d < 50*time.Second || d > 70*time.Second {
		t.Errorf("expected ~60s delay, got %v", d)
	}
}

func TestParseRetryAfterPastDate(t *testing.T) {
	past := time.Now().Add(-60 * time.Second).UTC().Format(http.TimeFormat)
	_, ok := parseRetryAfter(past)
	if ok {
		t.Error("expected ok=false for past HTTP date")
	}
}

func TestParseRetryAfterCapsAtMaxDelay(t *testing.T) {
	d, ok := parseRetryAfter("99999999")
	if !ok {
		t.Error("expected ok=true")
	}
	if d > maxRetryDelay {
		t.Errorf("expected delay capped at %v, got %v", maxRetryDelay, d)
	}
}

// ── parseRateLimitReset ───────────────────────────────────────────────────────

func TestParseRateLimitResetEmpty(t *testing.T) {
	_, ok := parseRateLimitReset("")
	if ok {
		t.Error("expected ok=false for empty header")
	}
}

func TestParseRateLimitResetFuture(t *testing.T) {
	future := time.Now().Add(60 * time.Second).Unix()
	header := fmt.Sprintf("%d", future)
	d, ok := parseRateLimitReset(header)
	if !ok {
		t.Error("expected ok=true for future unix timestamp")
	}
	if d < 50*time.Second || d > 70*time.Second {
		t.Errorf("expected ~60s delay, got %v", d)
	}
}

func TestParseRateLimitResetPast(t *testing.T) {
	past := time.Now().Add(-60 * time.Second).Unix()
	header := fmt.Sprintf("%d", past)
	_, ok := parseRateLimitReset(header)
	if ok {
		t.Error("expected ok=false for past timestamp")
	}
}

// ── GitLab component resolution ───────────────────────────────────────────────

func newFakeGitLabServer(tagSHA, commitSHA string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/repository/tags/") {
			if tagSHA == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"commit": map[string]string{"id": tagSHA},
			})
			return
		}
		if strings.Contains(r.URL.Path, "/repository/commits/") {
			if commitSHA == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"id": commitSHA})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestGitLabFetchTagSHA(t *testing.T) {
	srv := newFakeGitLabServer("abc1234def5678", "")
	defer srv.Close()

	r := NewGitLabResolver(srv.URL, "", nil)
	r.client = &http.Client{Transport: rewriteHostTransport{base: srv.URL}}

	sha, err := r.fetchTagSHA(testEncodedProject, testTagV1)
	if err != nil {
		t.Fatalf("fetchTagSHA: %v", err)
	}
	if sha != "abc1234def5678" {
		t.Errorf("expected abc1234def5678, got %q", sha)
	}
}

func TestGitLabFetchTagSHANotFound(t *testing.T) {
	srv := newFakeGitLabServer("", "")
	defer srv.Close()

	r := NewGitLabResolver(srv.URL, "", nil)
	r.client = &http.Client{Transport: rewriteHostTransport{base: srv.URL}}

	_, err := r.fetchTagSHA(testEncodedProject, testTagV1)
	if err == nil {
		t.Error("expected error for 404")
	}
}

func TestGitLabFetchCommitSHA(t *testing.T) {
	srv := newFakeGitLabServer("", "deadbeefcafe")
	defer srv.Close()

	r := NewGitLabResolver(srv.URL, "", nil)
	r.client = &http.Client{Transport: rewriteHostTransport{base: srv.URL}}

	sha, err := r.fetchCommitSHA(testEncodedProject, "main")
	if err != nil {
		t.Fatalf("fetchCommitSHA: %v", err)
	}
	if sha != "deadbeefcafe" {
		t.Errorf("expected deadbeefcafe, got %q", sha)
	}
}

func TestGitLabPinComponentAlreadyPinned(t *testing.T) {
	r := NewGitLabResolver(testGitLabCom, "", nil)

	content := "  - component: gitlab.com/group/project/name@abc1234def5678901234567890abcdef12345678 # v1.0.0\n"
	result, err := r.pinComponents(content)
	if err != nil {
		t.Fatalf(testPinComponentsFmt, err)
	}
	if result != content {
		t.Errorf("already-pinned component should be left unchanged, got:\n%s", result)
	}
}

func TestGitLabPinComponentSkipsVariablePrefix(t *testing.T) {
	r := NewGitLabResolver(testGitLabCom, "", nil)

	content := "  - component: $SPLIT_GLOBAL_COMPONENT_ROOT/group/project/name@v1.0.0\n"
	result, err := r.pinComponents(content)
	if err != nil {
		t.Fatalf(testPinComponentsFmt, err)
	}
	if result != content {
		t.Errorf("variable-prefixed component should be left unchanged, got:\n%s", result)
	}
}

func TestGitLabResolveHostVar(t *testing.T) {
	r := NewGitLabResolver("https://gitlab.example.com", "", nil)

	cases := []struct {
		input string
		want  string
	}{
		{"$CI_SERVER_FQDN/group/project/name", "gitlab.example.com/group/project/name"},
		{"$CI_SERVER_HOST/group/project/name", "gitlab.example.com/group/project/name"},
		{"$OTHER_VAR/group/project/name", "$OTHER_VAR/group/project/name"},
	}
	for _, c := range cases {
		got := r.resolveHostVar(c.input)
		if got != c.want {
			t.Errorf("resolveHostVar(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestGitLabName(t *testing.T) {
	r := NewGitLabResolver(testGitLabCom, "", nil)
	if r.Name() == "" {
		t.Error("expected non-empty Name()")
	}
}

// ── Name() methods ────────────────────────────────────────────────────────────

func TestProviderNames(t *testing.T) {
	cases := []struct {
		name string
		got  string
	}{
		{"GitHub", NewGitHubResolver("").Name()},
		{"Forgejo", NewForgejoResolver("", "").Name()},
		{"Dockerfile", NewDockerfileResolver().Name()},
		{"CircleCI", NewCircleCIResolver("").Name()},
		{"Bitbucket", NewBitbucketResolver().Name()},
		{"Woodpecker", NewWoodpeckerResolver().Name()},
		{"Compose", NewComposeResolver().Name()},
	}
	for _, c := range cases {
		if c.got == "" {
			t.Errorf("%s: Name() returned empty string", c.name)
		}
	}
}

// ── dockerfile.warnIfDrifted ──────────────────────────────────────────────────

func TestDockerfileWarnIfDriftedNoPanic(t *testing.T) {
	r := NewDockerfileResolver()
	// Already-pinned content — warnIfDrifted should not panic even if API fails.
	content := "FROM golang@sha256:abc123def456abc123def456abc123def456abc123def456abc123def456ab # golang:1.24\n"
	r.warnIfDrifted(content)
}

// ── github.fetchTagObjectSHA ──────────────────────────────────────────────────

func TestGitHubFetchTagObjectSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"object": map[string]string{"sha": testFakeSHA},
		})
	}))
	defer srv.Close()

	r := NewGitHubResolverWithClient("", &http.Client{Transport: rewriteHostTransport{base: srv.URL}})
	sha, err := r.fetchTagObjectSHA(srv.URL + "/repos/owner/repo/git/tags/abc123")
	if err != nil {
		t.Fatalf("fetchTagObjectSHA: %v", err)
	}
	if sha != testFakeSHA {
		t.Errorf(testExpectFmt, testFakeSHA, sha)
	}
}

// ── forgejo.fetchTagSHA ───────────────────────────────────────────────────────

func TestForgejoFetchTagSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"object": map[string]string{"sha": testFakeSHA}},
		})
	}))
	defer srv.Close()

	r := NewForgejoResolver(srv.URL, "")
	r.client = &http.Client{Transport: rewriteHostTransport{base: srv.URL}}
	sha, err := r.fetchTagSHA(srv.URL, testOwnerRepo, testTagV1)
	if err != nil {
		t.Fatalf("fetchTagSHA: %v", err)
	}
	if sha != testFakeSHA {
		t.Errorf(testExpectFmt, testFakeSHA, sha)
	}
}

func TestForgejoFetchTagSHANotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := NewForgejoResolver(srv.URL, "")
	r.client = &http.Client{Transport: rewriteHostTransport{base: srv.URL}}
	_, err := r.fetchTagSHA(srv.URL, testOwnerRepo, testTagV1)
	if err == nil {
		t.Error("expected error for 404")
	}
}

// ── gitlab.warnIfDrifted ──────────────────────────────────────────────────────

func TestGitLabWarnIfDriftedNoPanic(t *testing.T) {
	r := NewGitLabResolver(testGitLabCom, "", nil)
	content := "  - component: gitlab.com/group/project/name@abc1234def5678901234567890abcdef12345678 # v1.0.0\n"
	r.warnIfDrifted(content)
}

// ── gitlab.fetchComponentSHA error path ──────────────────────────────────────

func TestGitLabFetchComponentSHAInvalidPath(t *testing.T) {
	r := NewGitLabResolver(testGitLabCom, "", nil)
	_, err := r.fetchComponentSHA("no-dot/invalid", testTagV1)
	if err == nil {
		t.Error("expected error for invalid component path")
	}
}

// ── github.fetchSHA fallback path ─────────────────────────────────────────────

func TestGitHubFetchSHAFallbackToCommit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, testGitRefsTagsPath) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(r.URL.Path, "/commits/") {
			json.NewEncoder(w).Encode(map[string]string{"sha": testFakeSHA})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := NewGitHubResolverWithClient("", &http.Client{Transport: rewriteHostTransport{base: srv.URL}})
	sha, err := r.fetchSHA(testOwnerRepo, "main")
	if err != nil {
		t.Fatalf("fetchSHA fallback: %v", err)
	}
	if sha != testFakeSHA {
		t.Errorf(testExpectFmt, testFakeSHA, sha)
	}
}

func TestGitHubFetchTagSHAAnnotatedBadURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, testGitRefsTagsPath) {
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{
					"sha":  "tagobjectsha",
					"type": "tag",
					"url":  "http://untrusted.example.com/repos/owner/repo/git/tags/tagobjectsha",
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := NewGitHubResolverWithClient("", &http.Client{Transport: rewriteHostTransport{base: srv.URL}})
	_, err := r.fetchTagSHA(testOwnerRepo, testTagV1)
	if err == nil {
		t.Error("expected error for unexpected tag object URL")
	}
}

// ── forgejo.fetchSHA fallback ─────────────────────────────────────────────────

func TestForgejoFetchSHAFallbackToCommit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, testGitRefsTagsPath) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(r.URL.Path, "/commits/") {
			json.NewEncoder(w).Encode(map[string]string{"sha": testFakeSHA})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := NewForgejoResolver(srv.URL, "")
	r.client = &http.Client{Transport: rewriteHostTransport{base: srv.URL}}
	sha, err := r.fetchSHA(testOwnerRepo, "main")
	if err != nil {
		t.Fatalf("fetchSHA: %v", err)
	}
	if sha != testFakeSHA {
		t.Errorf(testExpectFmt, testFakeSHA, sha)
	}
}

// ── retryDelay ────────────────────────────────────────────────────────────────

func TestRetryDelayRetryAfterSeconds(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Retry-After": []string{"30"}}}
	d := retryDelay(resp)
	if d != 30*time.Second {
		t.Errorf("expected 30s, got %v", d)
	}
}

func TestRetryDelayRateLimitReset(t *testing.T) {
	future := time.Now().Add(2 * time.Minute).Unix()
	resp := &http.Response{Header: http.Header{"X-Ratelimit-Reset": []string{fmt.Sprintf("%d", future)}}}
	d := retryDelay(resp)
	if d < 90*time.Second || d > 150*time.Second {
		t.Errorf("expected ~2m, got %v", d)
	}
}

func TestRetryDelayFallback(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	d := retryDelay(resp)
	if d != 60*time.Second {
		t.Errorf("expected 60s fallback, got %v", d)
	}
}

// ── warnIfDrifted (dockerfile) ────────────────────────────────────────────────

func TestDockerfileWarnIfDriftedNoDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "token") {
			json.NewEncoder(w).Encode(map[string]string{"token": "tok"})
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:aabbcc")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := NewDockerfileResolver()
	r.docker.client = &http.Client{Transport: rewriteHostTransport{base: srv.URL}}
	content := "# alpine:3.20\nFROM alpine@sha256:aabbcc\n"
	_, err := r.Resolve(content, false, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

// ── pinComponents (gitlab) ────────────────────────────────────────────────────

func TestGitLabPinComponentsAlreadyPinned(t *testing.T) {
	r := NewGitLabResolver(testGitLabCom, "", nil)
	content := "  - component: gitlab.com/group/project/comp@abc1234567890123456789012345678901234567890\n"
	result, err := r.pinComponents(content)
	if err != nil {
		t.Fatalf(testPinComponentsFmt, err)
	}
	if result != content {
		t.Errorf("already-pinned component should not change; got %q", result)
	}
}

func TestGitLabPinComponentsWithFakeServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/repository/tags/") {
			json.NewEncoder(w).Encode(map[string]any{
				"commit": map[string]string{"id": testFakeSHA},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := NewGitLabResolver(srv.URL, "", nil)
	r.client = &http.Client{Transport: rewriteHostTransport{base: srv.URL}}
	content := "  - component: gitlab.com/group/project/comp@v1.0.0\n"
	result, err := r.pinComponents(content)
	if err != nil {
		t.Fatalf(testPinComponentsFmt, err)
	}
	if !strings.Contains(result, testFakeSHA) {
		t.Errorf("expected SHA %q in result, got: %q", testFakeSHA, result)
	}
}
