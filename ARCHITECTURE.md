# Architecture

This document describes the design of Shapin — the actions, actors, and data flow involved in scanning and pinning CI/CD files.

## Overview

Shapin is a CLI tool that walks a directory tree, identifies CI/CD configuration files, and rewrites floating tags (e.g. `@v4`, `:latest`) to immutable SHAs by querying upstream registries and APIs.

```
User
 └─▶ CLI (cmd/shapin)
       └─▶ Scanner
             ├─▶ Provider (GitHub Actions)  ─▶ GitHub API
             ├─▶ Provider (GitLab CI)       ─▶ GitLab API + Registry API
             ├─▶ Provider (Forgejo)         ─▶ Forgejo API
             ├─▶ Provider (CircleCI)        ─▶ Registry API
             ├─▶ Provider (Bitbucket)       ─▶ Registry API
             ├─▶ Provider (Woodpecker)      ─▶ Registry API
             ├─▶ Provider (Dockerfile)      ─▶ Registry API
             └─▶ Provider (Docker Compose)  ─▶ Registry API
```

## Actors

| Actor | Description |
|---|---|
| **User** | Invokes the CLI with flags and a target path |
| **CLI** (`cmd/shapin`) | Parses flags, loads config, constructs the provider list, and delegates to the Scanner |
| **Scanner** (`internal/scanner`) | Walks the file tree, matches files to providers, dispatches resolution concurrently, writes results |
| **Provider** (`internal/providers`) | Implements file matching and content rewriting for a specific CI/CD system |
| **Docker Resolver** | Shared component used by all providers to resolve `image:tag` → `image@sha256:digest` via the Docker Registry HTTP API v2 |
| **GitHub API** | External: resolves `owner/repo@tag` → commit SHA for GitHub Actions `uses:` refs |
| **GitLab API** | External: resolves GitLab CI component refs and bare version variables |
| **Forgejo API** | External: resolves Forgejo Actions `uses:` refs |
| **Docker Registry** | External: resolves image tags to content-addressable digests (Docker Hub, GHCR, Quay.io, etc.) |

## Data flow

1. **CLI** reads flags and optional `.shapin.json` config, then calls `scanner.Run(cfg)`.
2. **Scanner** walks the directory tree, skipping `node_modules`, `.git`, `vendor`, `dist`.
3. For each file, the Scanner asks each **Provider** in order whether it matches (`IsMatch`). The first match wins.
4. Matched files are dispatched to a worker pool (8 concurrent workers).
5. Each worker reads the file and calls `provider.Resolve(content, pinActions, pinImages)`.
6. The **Provider** applies regex-based rewriting:
   - Action refs (`uses: owner/repo@tag`) → resolved via the upstream VCS API to a commit SHA.
   - Image refs (`image: name:tag`, `FROM name:tag`) → resolved via the Docker Registry API to a content digest.
   - Already-pinned refs are checked for drift and a warning is emitted if the tag has moved.
7. If content changed, the Scanner writes the updated file (or prints a diff in dry-run mode).
8. Results are reported in text, JSON, or SARIF format.

## Provider interface

Every provider implements `contract.Provider`:

```go
type Provider interface {
    Name() string
    IsMatch(relPath string) bool
    Resolve(content string, pinActions, pinImages bool) (string, error)
}
```

Adding a new provider requires only implementing this interface and registering it in `internal/scanner/runner.go`. No existing code needs modification.

## Concurrency model

The Scanner runs up to 8 file-processing goroutines concurrently. Each Provider uses a `syncCache` (a mutex-protected map) to deduplicate API calls — if two files reference the same action or image tag, only one HTTP request is made.

## External API interactions

| Provider | API | Auth |
|---|---|---|
| GitHub Actions | `api.github.com` | Optional `--github-token` (Bearer) |
| GitLab CI | Configurable host (default `gitlab.com`) | Optional `--gitlab-token` (Bearer) |
| Forgejo | Configurable host | Optional `--forgejo-token` (Bearer) |
| All image providers | Docker Registry HTTP API v2 | Optional `--registry-token` |

All HTTP calls go through `doWithRetry`, which honours `Retry-After` and `X-RateLimit-Reset` headers and retries up to 3 times on HTTP 429 or 503.
