package providers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newFakeDigestClient returns an *http.Client backed by a test server that
// responds to any /v2/.*/manifests/.* request with the given digest header.
// Used by fuzz targets to avoid real network calls.
func newFakeDigestClient(digest string) *http.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			w.Header().Set("Docker-Content-Digest", digest)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	return &http.Client{
		Transport: rewriteHostTransport{base: srv.URL},
	}
}

// FuzzExtractStem exercises the version-marker stripping logic against
// arbitrary variable names. It must never panic.
func FuzzExtractStem(f *testing.F) {
	f.Add("TF_VERSION")
	f.Add("VERSION_TF")
	f.Add("TF_TAG")
	f.Add("TAG_TF")
	f.Add("TF_DIGEST")
	f.Add("NODE")
	f.Add("")
	f.Add("_")
	f.Add("VERSION")
	f.Add("TAG")
	f.Fuzz(func(t *testing.T, key string) {
		extractStem(key)
	})
}

// FuzzToDigestKey exercises the version-marker renaming logic against
// arbitrary variable names. It must never panic.
func FuzzToDigestKey(f *testing.F) {
	f.Add("TF_VERSION")
	f.Add("VERSION_TF")
	f.Add("TF_TAG")
	f.Add("TAG_TF")
	f.Add("NODE_VERSION")
	f.Add("")
	f.Add("_")
	f.Add("DIGEST")
	f.Fuzz(func(t *testing.T, key string) {
		toDigestKey(key)
	})
}

// FuzzIsSHA exercises the SHA detection logic against arbitrary strings.
// It must never panic.
func FuzzIsSHA(f *testing.F) {
	f.Add("aabbccdd11223344556677889900aabbccdd1100")
	f.Add("sha256:abc123")
	f.Add("v4")
	f.Add("main")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		isSHA(s)
	})
}

// FuzzDockerResolveImages exercises the Docker image regex rewriting against
// arbitrary YAML/text content. No HTTP calls are made — the resolver uses a
// fake server that always returns a fixed digest.
func FuzzDockerResolveImages(f *testing.F) {
	f.Add("image: alpine:3.18\n")
	f.Add("image: 'nginx:latest'\n")
	f.Add("image: ubuntu:22.04\n")
	f.Add("# image: commented:out\n")
	f.Add("")
	f.Add("image: @sha256:abc\n")
	f.Add("notanimage: foo:bar\n")
	f.Fuzz(func(t *testing.T, content string) {
		r := newDockerResolver("")
		// Replace the HTTP client with one that always returns a fixed digest
		// so the fuzzer exercises the parsing/regex paths, not networking.
		r.client = newFakeDigestClient("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		r.resolveImages(content)
	})
}

// FuzzDockerfilePinFrom exercises the Dockerfile FROM-line rewriting against
// arbitrary content. No HTTP calls are made.
func FuzzDockerfilePinFrom(f *testing.F) {
	f.Add("FROM alpine:3.18\n")
	f.Add("FROM ubuntu:22.04 AS builder\n")
	f.Add("FROM scratch\n")
	f.Add("")
	f.Add("FROM alpine@sha256:abc # 3.18\n")
	f.Add("RUN echo hello\n")
	f.Fuzz(func(t *testing.T, content string) {
		r := NewDockerfileResolver()
		r.docker.client = newFakeDigestClient("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		r.pinFrom(content)
	})
}
