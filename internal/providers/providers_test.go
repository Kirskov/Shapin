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
	ghWorkflowCI         = ".github/workflows/ci.yml"
	ghWorkflowSkip       = ".github/workflows/skip.yml"
	ghWorkflowDir        = "/.github/workflows"
	ciYML                = "/ci.yml"
	checkoutV4Line       = "      - uses: actions/checkout@v4\n"
	testFakeSHA          = "aabbccdd11223344556677889900aabbccdd1100"
	gitlabCom            = "https://gitlab.com"
	gitlabCIYML          = ".gitlab-ci.yml"
	manifestsPath        = "/manifests/"
	dockerDigestHeader   = "Docker-Content-Digest"
	wantDigestInOutput   = "expected digest in output, got:\n%s"
	wantTagAsComment     = "expected original tag as comment, got:\n%s"
	wantContentUnchanged = "expected content unchanged when pinImages=false, got:\n%s"
	wantSHAInOutput      = "expected SHA in output, got:\n%s"
	gitRefsTagsPath      = "/git/refs/tags/"
	commitsPath          = "/commits/"
	trivyVersion         = "0.69.3"
	wantTrivyTagComment  = "# " + trivyVersion
	imageTerraform       = "hashicorp/terraform"
	imageTrivy           = "aquasec/trivy"
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
	p := NewGitLabResolver(gitlabCom, "", nil)
	cases := []struct {
		path string
		want bool
	}{
		// root-level files
		{gitlabCIYML, true},
		{".gitlab-ci.yaml", true},
		{".gitlab-ci-build.yml", true},
		{".gitlab/ci.yml", true},
		{".gitlab/templates/deploy.yaml", true},
		// monorepo: project subdirectory
		{"my-service/.gitlab-ci.yml", true},
		{"my-service/.gitlab-ci.yaml", true},
		{"my-service/.gitlab-ci-build.yml", true},
		{"my-service/.gitlab/logic.yml", true},
		{"my-service/.gitlab/templates/deploy.yaml", true},
		// non-matches
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

// ── GitLab version input pinning ─────────────────────────────────────────────

func TestGitLabPinsBuiltinVersionInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:terraform01")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewGitLabResolver(gitlabCom, "", nil)
	p.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := "      TF_VERSION: \"1.14.8\"\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "TF_DIGEST") {
		t.Errorf("expected key renamed to TF_DIGEST, got:\n%s", got)
	}
	if !strings.Contains(got, "sha256:terraform01") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# hashicorp/terraform:1.14.8") {
		t.Errorf(wantTagAsComment, got)
	}
}

func TestGitLabPinsUserMappingOverridesBuiltin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:customtf01")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewGitLabResolver(gitlabCom, "", map[string]string{"TF": "myregistry.example.com/terraform"})
	p.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := "      TF_VERSION: \"1.14.8\"\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:customtf01") {
		t.Errorf(wantDigestInOutput, got)
	}
}

func TestGitLabSkipsVersionInputWithDollarSign(t *testing.T) {
	p := NewGitLabResolver(gitlabCom, "", nil)
	content := "      TF_VERSION: \"$SOME_VAR\"\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("expected CI variable interpolation to be skipped, got:\n%s", got)
	}
}

func TestGitLabSkipsVersionInputAlreadyDigest(t *testing.T) {
	p := NewGitLabResolver(gitlabCom, "", nil)
	content := "      TF_VERSION: \"sha256:abc123\"\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("expected already-digested value to be skipped, got:\n%s", got)
	}
}

func TestGitLabRepinsDigestKeyWithBareVersion(t *testing.T) {
	// TF_DIGEST: "1.14.8" (bare version, not yet a digest) should be re-pinned
	// to TF_DIGEST: "sha256:... # 1.14.8" — this is the upgrade workflow.
	p := NewGitLabResolver(gitlabCom, "", nil)
	content := "  TF_DIGEST: \"1.14.8\"\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:") {
		t.Errorf("expected TF_DIGEST bare version to be pinned, got:\n%s", got)
	}
	if !strings.Contains(got, "# hashicorp/terraform:1.14.8") {
		t.Errorf("expected original version in comment, got:\n%s", got)
	}
	if !strings.Contains(got, "TF_DIGEST:") {
		t.Errorf("expected key to remain TF_DIGEST, got:\n%s", got)
	}
}

func TestLookupStem(t *testing.T) {
	p := NewGitLabResolver(gitlabCom, "", nil)
	cases := []struct {
		stem string
		want string // "" means no match expected
	}{
		{"NODE", "node"},
		{"NODE_IMAGE", "node"},    // intermediate segment stripped
		{"NODE_RUNNER", "node"},   // any unknown suffix stripped
		{"TF", imageTerraform},
		{"TF_IMAGE", imageTerraform},
		{"TRIVY", imageTrivy},
		{"TRIVY_IMAGE", imageTrivy},
		{"FAKESTEM", ""},           // unknown stem, no match
		{"FAKESTEM_IMAGE", ""},     // unknown even after stripping
		{"FAKESTEM_IMAGE_DIGEST", ""},
	}
	for _, c := range cases {
		got := p.lookupStem(c.stem)
		if got != c.want {
			t.Errorf("lookupStem(%q) = %q, want %q", c.stem, got, c.want)
		}
	}
}

func TestGitLabPinsVersionInputWithIntermediateSuffix(t *testing.T) {
	// NODE_IMAGE_DIGEST: "24.14.1-alpine3.23" should be pinned even though
	// the stem is NODE_IMAGE, not NODE — lookupStem must strip _IMAGE to find NODE.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:node000001")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewGitLabResolver(gitlabCom, "", nil)
	p.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := "      NODE_IMAGE_DIGEST: \"24.14.1-alpine3.23\"\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:node000001") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# node:24.14.1-alpine3.23") {
		t.Errorf(wantTagAsComment, got)
	}
}

func TestGitLabMixedPinnedAndUnpinnedInputs(t *testing.T) {
	// Regression test: when some inputs are already pinned (sha256:...) and one
	// is not (NODE_IMAGE_DIGEST: bare version), shapin must pin the unpinned one
	// and not report "everything already pinned".
	digests := map[string]string{
		"node": "sha256:node000002",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, digestForPath(r.URL.Path, digests))
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewGitLabResolver(gitlabCom, "", nil)
	p.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := `include:
  - component: $SPLIT_GLOBAL_COMPONENT_ROOT/node-catalogue/node-lambda-base@2.2.5
    inputs:
      TF_IMAGE_DIGEST: 'sha256:6bbb82d575aa7bd4f0a2c6e3a0838ab9590426c08a71d7a2783643f01004d356' # 1.13.5
      TRIVY_IMAGE_DIGEST: "sha256:bcc376de8d77cfe086a917230e818dc9f8528e3c852f7b1aff648949b6258d1c" # 0.69.3
      NODE_IMAGE_DIGEST: 24.14.1-alpine3.23
`
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:node000002") {
		t.Errorf("expected NODE_IMAGE_DIGEST to be pinned, got:\n%s", got)
	}
	if !strings.Contains(got, "# node:24.14.1-alpine3.23") {
		t.Errorf(wantTagAsComment, got)
	}
	// Already-pinned inputs must be left untouched
	if !strings.Contains(got, "sha256:6bbb82d575aa7bd4f0a2c6e3a0838ab9590426c08a71d7a2783643f01004d356") {
		t.Errorf("expected TF_IMAGE_DIGEST to remain unchanged, got:\n%s", got)
	}
}

func TestExtractStem(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		// suffix form
		{"TF_VERSION", "TF"},
		{"TF_TAG", "TF"},
		{"TF_DIGEST", "TF"},
		{"NODE_VERSION", "NODE"},
		{"tf_version", "TF"}, // case-insensitive
		// prefix form
		{"VERSION_TF", "TF"},
		{"TAG_TF", "TF"},
		{"DIGEST_NODE", "NODE"},
		// no marker
		{"NOTSUFFIX", ""},
		{"VERSION", ""}, // marker only, no stem
		{"TAG", ""},
	}
	for _, c := range cases {
		if got := extractStem(c.key); got != c.want {
			t.Errorf("extractStem(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

func digestForPath(path string, digests map[string]string) string {
	for image, digest := range digests {
		if strings.Contains(path, image) {
			return digest
		}
	}
	return "sha256:fallback0"
}

func TestToDigestKey(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{"TF_VERSION", "TF_DIGEST"},
		{"TF_TAG", "TF_DIGEST"},
		{"TF_DIGEST", "TF_DIGEST"}, // already DIGEST, unchanged
		{"NODE_VERSION", "NODE_DIGEST"},
		{"VERSION_TF", "DIGEST_TF"},
		{"TAG_NODE", "DIGEST_NODE"},
		{"NOMARKER", "NOMARKER"}, // no marker, unchanged
	}
	for _, c := range cases {
		if got := toDigestKey(c.key); got != c.want {
			t.Errorf("toDigestKey(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestGitLabRealWorldComponentInputs(t *testing.T) {
	// Each image gets a distinct digest so we can assert each one was resolved.
	digests := map[string]string{
		imageTerraform: "sha256:tf000001",
		imageTrivy:       "sha256:trivy001",
		"node":                "sha256:node0001",
		"alpine":              "sha256:alpine01",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, digestForPath(r.URL.Path, digests))
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewGitLabResolver(gitlabCom, "", nil)
	p.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := `variables:
  DEPLOY_ENV:
    description: 'Deploy on the environment'
    value: 'dev'
    options:
      - 'dev'
      - 'int'
      - 'uat'
      - 'stg'
      - 'prd'
  INFRA_DIR: ./infra/
  WORKING_DIR: .
  PACKAGE_JSON_DIR: ./lambdas/
  IS_PACKAGE_BUILD_NEEDED: 'false'

include:
  - component: $SPLIT_GLOBAL_COMPONENT_ROOT/node-catalogue/node-lambda-base@2.1.4
    inputs:
      TFSTATE_KEY_PATH: 'lambdas-ingestions-stepfunction'
      SCAN_PREFIX: 'lambdas'
      TF_VERSION: '1.13.5'
      TRIVY_VERSION: "0.69.1"
      NODE_VERSION: '24.13.0'
      ALPINE_VERSION: '3.23'
`
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}

	// Version inputs must be renamed to DIGEST and pinned.
	pinned := []struct{ digestKey, comment string }{
		{"TF_DIGEST", "# hashicorp/terraform:1.13.5"},
		{"TRIVY_DIGEST", "# aquasec/trivy:0.69.1"},
		{"NODE_DIGEST", "# node:24.13.0"},
		{"ALPINE_DIGEST", "# alpine:3.23"},
	}
	for _, c := range pinned {
		if !strings.Contains(got, c.digestKey+":") {
			t.Errorf("expected key %s in output, got:\n%s", c.digestKey, got)
		}
		if !strings.Contains(got, c.comment) {
			t.Errorf("expected comment %s in output, got:\n%s", c.comment, got)
		}
	}
	// Original _VERSION keys must be gone.
	for _, key := range []string{"TF_VERSION", "TRIVY_VERSION", "NODE_VERSION", "ALPINE_VERSION"} {
		if strings.Contains(got, key+":") {
			t.Errorf("expected %s to be renamed, still present in output:\n%s", key, got)
		}
	}

	// Non-version keys must be unchanged.
	unchanged := []string{
		"DEPLOY_ENV:",
		"INFRA_DIR: ./infra/",
		"WORKING_DIR: .",
		"IS_PACKAGE_BUILD_NEEDED: 'false'",
		"TFSTATE_KEY_PATH: 'lambdas-ingestions-stepfunction'",
		"SCAN_PREFIX: 'lambdas'",
		"component: $SPLIT_GLOBAL_COMPONENT_ROOT/node-catalogue/node-lambda-base@2.1.4",
	}
	for _, s := range unchanged {
		if !strings.Contains(got, s) {
			t.Errorf("expected %q to be unchanged in output, got:\n%s", s, got)
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
		{circleciConfigs[0], true},
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
		t.Errorf(wantContentUnchanged, got)
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
		t.Errorf(wantContentUnchanged, got)
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

func TestGitLabPinsImageWithDependencyProxyPrefix(t *testing.T) {
	// Regression test: when an image uses a dependency proxy variable prefix
	// (e.g. image: ${CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX}/alpine:3.20),
	// the pinned output must preserve the proxy prefix — it must not be stripped.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:alpine001")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewGitLabResolver(gitlabCom, "", nil)
	p.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "brace syntax group prefix",
			content: "  image: ${CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX}/alpine:3.20\n",
			want:    "${CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX}/alpine@sha256:alpine001",
		},
		{
			name:    "bare dollar syntax",
			content: "  image: $CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX/alpine:3.20\n",
			want:    "$CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX/alpine@sha256:alpine001",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := p.Resolve(c.content, false, true)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("expected proxy prefix preserved in output, want substring %q, got:\n%s", c.want, got)
			}
			if !strings.Contains(got, "# 3.20") {
				t.Errorf(wantTagAsComment, got)
			}
		})
	}
}

// ── GitLab extended image syntax (image: {name: ...}) ────────────────────────

func TestGitLabPinsImageNameSubkey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:namekey01")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewGitLabResolver(gitlabCom, "", nil)
	p.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := "image:\n  name: myregistry.example.com/myimage:1.0.0\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:namekey01") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# 1.0.0") {
		t.Errorf(wantTagAsComment, got)
	}
}

func TestGitLabPinsImageNameSubkeyWithEntrypoint(t *testing.T) {
	// Extended image syntax with entrypoint — name: must be pinned, entrypoint untouched.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:namekey02")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewGitLabResolver(gitlabCom, "", nil)
	p.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := "image:\n  name: myregistry.example.com/myimage:2.3.4\n  entrypoint: [\"/bin/sh\", \"-c\"]\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:namekey02") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# 2.3.4") {
		t.Errorf(wantTagAsComment, got)
	}
	if !strings.Contains(got, "entrypoint: [\"/bin/sh\", \"-c\"]") {
		t.Errorf("expected entrypoint to be preserved, got:\n%s", got)
	}
}

func TestGitLabSkipsImageNameSubkeyLatest(t *testing.T) {
	p := NewGitLabResolver(gitlabCom, "", nil)
	content := "image:\n  name: myregistry.example.com/myimage:latest\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("expected 'latest' in name: to be skipped, got:\n%s", got)
	}
}

func TestGitLabSkipsImageNameSubkeyAlreadyPinned(t *testing.T) {
	p := NewGitLabResolver(gitlabCom, "", nil)
	content := "image:\n  name: myregistry.example.com/myimage@sha256:abc123\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("expected already-pinned name: to be skipped, got:\n%s", got)
	}
}

// ── GitLab services: block ───────────────────────────────────────────────────

func TestGitLabPinsServiceBareItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:svc00001")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewGitLabResolver(gitlabCom, "", nil)
	p.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := "services:\n  - postgres:15\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:svc00001") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# 15") {
		t.Errorf(wantTagAsComment, got)
	}
}

func TestGitLabPinsServiceNameSubkey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:svc00002")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewGitLabResolver(gitlabCom, "", nil)
	p.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := "services:\n  - name: redis:7\n    alias: cache\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:svc00002") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# 7") {
		t.Errorf(wantTagAsComment, got)
	}
}

func TestGitLabSkipsServiceBareItemLatest(t *testing.T) {
	p := NewGitLabResolver(gitlabCom, "", nil)
	content := "services:\n  - postgres:latest\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("expected 'latest' service item to be skipped, got:\n%s", got)
	}
}

func TestGitLabSkipsServiceBareItemAlreadyPinned(t *testing.T) {
	p := NewGitLabResolver(gitlabCom, "", nil)
	content := "services:\n  - postgres@sha256:abc123\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("expected already-pinned service item to be skipped, got:\n%s", got)
	}
}

func TestGitLabPinsMultipleServicesInBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:svc00003")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewGitLabResolver(gitlabCom, "", nil)
	p.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := "services:\n  - postgres:15\n  - redis:7\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "sha256:svc00003") != 2 {
		t.Errorf("expected both services to be pinned, got:\n%s", got)
	}
}

func TestGitLabPinsServiceInJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:svc00004")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewGitLabResolver(gitlabCom, "", nil)
	p.docker.client = &http.Client{Transport: rewriteHost(srv.URL)}

	content := "test:\n  image: golang:1.24\n  services:\n    - postgres:15\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:svc00004") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# 15") {
		t.Errorf(wantTagAsComment, got)
	}
}

func TestGitLabServicesBlockDoesNotLeakIntoNextKey(t *testing.T) {
	p := NewGitLabResolver(gitlabCom, "", nil)
	// `script:` follows `services:` — the `- not-an-image:value` line must not be touched.
	content := "services:\n  - postgres:latest\nscript:\n  - not-an-image:value\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "- not-an-image:value") {
		t.Errorf("expected script item to be untouched, got:\n%s", got)
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

// ── stripDependencyProxyPrefix ───────────────────────────────────────────────

func TestStripDependencyProxyPrefix(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		changed bool
	}{
		{
			name:    "brace syntax group prefix",
			input:   "${CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX}/node",
			want:    "node",
			changed: true,
		},
		{
			name:    "brace syntax direct group prefix",
			input:   "${CI_DEPENDENCY_PROXY_DIRECT_GROUP_IMAGE_PREFIX}/alpine",
			want:    "alpine",
			changed: true,
		},
		{
			name:    "bare dollar syntax",
			input:   "$CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX/python",
			want:    "python",
			changed: true,
		},
		{
			name:    "no proxy prefix",
			input:   "node",
			want:    "node",
			changed: false,
		},
		{
			name:    "unrelated variable",
			input:   "${SOME_OTHER_VAR}/node",
			want:    "${SOME_OTHER_VAR}/node",
			changed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := stripDependencyProxyPrefix(tc.input)
			if got != tc.want {
				t.Errorf("content: got %q, want %q", got, tc.want)
			}
			if changed != tc.changed {
				t.Errorf("changed: got %v, want %v", changed, tc.changed)
			}
		})
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

// ── Woodpecker CI ─────────────────────────────────────────────────────────────

func TestIsWoodpeckerCI(t *testing.T) {
	p := NewWoodpeckerResolver()
	cases := []struct {
		path string
		want bool
	}{
		{".woodpecker.yml", true},
		{".woodpecker.yaml", true},
		{".woodpecker/build.yml", true},
		{".woodpecker/deploy.yaml", true},
		{".woodpecker/sub/pipeline.yml", true},
		{".woodpecker-other.yml", false},
		{ghWorkflowCI, false},
		{gitlabCIYML, false},
	}
	for _, c := range cases {
		if got := p.IsMatch(c.path); got != c.want {
			t.Errorf("Woodpecker IsMatch(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestWoodpeckerPinsImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:woodpecker01")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewWoodpeckerResolver()
	p.setClient(&http.Client{Transport: rewriteHost(srv.URL)})

	content := "      image: myregistry.example.com/myimage:1.5.0\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:woodpecker01") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# 1.5.0") {
		t.Errorf(wantTagAsComment, got)
	}
}

func TestWoodpeckerSkipsWhenPinImagesFalse(t *testing.T) {
	p := NewWoodpeckerResolver()
	content := "      image: myregistry.example.com/myimage:1.5.0\n"
	got, err := p.Resolve(content, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf(wantContentUnchanged, got)
	}
}

// ── Dockerfile ────────────────────────────────────────────────────────────────

func TestIsDockerfile(t *testing.T) {
	p := NewDockerfileResolver()
	cases := []struct {
		path string
		want bool
	}{
		{"Dockerfile", true},
		{"Dockerfile.prod", true},
		{"service.dockerfile", true},
		{"service.Dockerfile", true},
		{"services/app/Dockerfile", true},
		{"infra/docker/Dockerfile.prod", true},
		{"a/b/c/Dockerfile", true},
		{"docker-compose.yml", false},
		{ghWorkflowCI, false},
	}
	for _, c := range cases {
		if got := p.IsMatch(c.path); got != c.want {
			t.Errorf("Dockerfile IsMatch(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestDockerfilePinsFrom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:dockerfile01")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewDockerfileResolver()
	p.setClient(&http.Client{Transport: rewriteHost(srv.URL)})

	content := "FROM myregistry.example.com/myimage:1.0.0 AS builder\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:dockerfile01") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# 1.0.0") {
		t.Errorf(wantTagAsComment, got)
	}
	// AS alias must be preserved
	if !strings.Contains(got, "AS builder") {
		t.Errorf("expected AS alias to be preserved, got:\n%s", got)
	}
}

func TestDockerfileSkipsFromScratch(t *testing.T) {
	p := NewDockerfileResolver()
	content := "FROM scratch\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("expected FROM scratch to be skipped, got:\n%s", got)
	}
}

func TestDockerfileSkipsWhenPinImagesFalse(t *testing.T) {
	p := NewDockerfileResolver()
	content := "FROM myregistry.example.com/myimage:1.0.0\n"
	got, err := p.Resolve(content, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf(wantContentUnchanged, got)
	}
}

// ── Docker Compose ────────────────────────────────────────────────────────────

func TestIsDockerCompose(t *testing.T) {
	p := NewComposeResolver()
	cases := []struct {
		path string
		want bool
	}{
		{"docker-compose.yml", true},
		{"docker-compose.yaml", true},
		{"docker-compose.prod.yml", true},
		{"compose.yml", true},
		{"compose.yaml", true},
		{"infra/docker-compose.yml", true},
		{"deploy/prod/docker-compose.override.yml", true},
		{"services/compose.yaml", true},
		{"docker-compose-other.yml", false},
		{ghWorkflowCI, false},
		{"Dockerfile", false},
	}
	for _, c := range cases {
		if got := p.IsMatch(c.path); got != c.want {
			t.Errorf("Compose IsMatch(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestComposePinsImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, manifestsPath) {
			w.Header().Set(dockerDigestHeader, "sha256:compose01")
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake"})
	}))
	defer srv.Close()

	p := NewComposeResolver()
	p.setClient(&http.Client{Transport: rewriteHost(srv.URL)})

	content := "    image: myregistry.example.com/myimage:2.0.0\n"
	got, err := p.Resolve(content, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sha256:compose01") {
		t.Errorf(wantDigestInOutput, got)
	}
	if !strings.Contains(got, "# 2.0.0") {
		t.Errorf(wantTagAsComment, got)
	}
}

func TestComposeSkipsWhenPinImagesFalse(t *testing.T) {
	p := NewComposeResolver()
	content := "    image: myregistry.example.com/myimage:2.0.0\n"
	got, err := p.Resolve(content, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf(wantContentUnchanged, got)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
