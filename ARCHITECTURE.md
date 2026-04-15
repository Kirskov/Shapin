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
| **GitLab API** | External: resolves GitLab CI component refs and bare version variables; handles multi-document files (spec: preamble + pipeline body) |
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
   - GitLab `spec: inputs:` defaults → the nested `default:` version is pinned and the `description:` field updated.
   - Already-pinned refs are checked for drift and a warning is emitted if the tag has moved.
   - Some providers insert lines (e.g. the Dockerfile provider inserts a `# image:tag` comment above `FROM`).
7. If content changed, the Scanner computes an LCS-based diff and writes the updated file (or prints the diff in dry-run mode).
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

## External software interfaces

### CLI

Shapin's primary interface is the command-line. All options are documented in the [Flags](README.md#flags) section of the README.

**Inputs:**
- `--path` — directory to scan (default: `.`)
- `--pin-refs` / `--pin-images` — controls which ref types are pinned
- `--dry-run` — when `true`, no files are written
- `--format` — output format: `text` (default), `json`, or `sarif`
- `--output` — redirect output to a file instead of stdout
- `--exclude` — glob patterns of files to skip
- Token flags / environment variables for authenticating to upstream APIs

**Outputs:**
- **stdout** (or `--output` file): human-readable diff (text), structured change list (JSON), or SARIF 2.1.0 report
- **stderr**: warnings (drift, branch refs, resolution failures) — never mixed into structured output
- **exit code `0`**: success (files may or may not have changed)
- **exit code non-zero**: fatal error (unreadable file, API failure on a pinned ref)

### Config file (`.shapin.json`)

All CLI flags can be persisted in a `.shapin.json` file at the scan root. CLI flags always take precedence. Full schema documented in [Config file](README.md#config-file).

```json
{
  "dry-run": false,
  "pin-refs": true,
  "pin-images": true,
  "exclude": ["path/to/skip.yml"],
  "github-token": "...",
  "gitlab-host": "https://gitlab.example.com",
  "gitlab-token": "...",
  "forgejo-host": "https://forgejo.example.com",
  "forgejo-token": "...",
  "tag-mappings": { "MY_TOOL": "myorg/mytool" }
}
```

### JSON output schema

When `--format json` is used, stdout is a JSON array:

```json
[
  {
    "path": ".github/workflows/ci.yml",
    "changes": [
      {
        "line": 12,
        "old": "uses: actions/checkout@v4",
        "new": "uses: actions/checkout@abc1234...  # v4"
      }
    ]
  }
]
```

### SARIF output schema

When `--format sarif` is used, stdout is a [SARIF 2.1.0](https://docs.oasis-open.org/sarif/sarif/v2.1.0/sarif-v2.1.0.html) document suitable for upload to GitHub Code Scanning. Each result identifies the file path and line number of an unpinned ref.
