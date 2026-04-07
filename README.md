# Shapin

Pin floating tags in CI workflow files to immutable SHAs, making your pipelines reproducible and immune to tag mutation attacks.

## What it does

| Reference type | Before | After |
|---|---|---|
| GitHub Action | `uses: actions/checkout@v4` | `uses: actions/checkout@abc1234... # v4` |
| Docker image (`image:`) | `image: maildev/maildev:2.2.1` | `image: maildev/maildev@sha256:180ef5... # 2.2.1` |
| Dockerfile `FROM` | `FROM golang:1.24-alpine AS builder` | `FROM golang@sha256:8bee19... # 1.24-alpine AS builder` |
| GitLab TAG input | `TRIVY_TAG: aquasec/trivy:0.69.3` | `TRIVY_DIGEST: aquasec/trivy@sha256:eafae... # 0.69.3` |
| GitLab mapped input | `TRIVY_VERSION: "0.69.3"` | `TRIVY_DIGEST: aquasec/trivy@sha256:eafae... # 0.69.3` |

Already-pinned refs (SHA or digest) are left untouched.

## Supported files

The tool scans recursively under `--path`, skipping `node_modules`, `.git`, `vendor`, and `dist`.

- **GitHub Actions**: any `.yml`/`.yaml` file inside `.github/workflows/` (and subdirectories)
- **GitLab CI**:
  - `.gitlab-ci.yml` / `.gitlab-ci.yaml` / `.gitlab-ci-*.yml` at the root
  - Any `.yml`/`.yaml` file inside `.gitlab/` and its subdirectories
- **CircleCI**: `.circleci/config.yml` / `.circleci/config.yaml`
- **Bitbucket Pipelines**: `bitbucket-pipelines.yml` / `bitbucket-pipelines.yaml`
- **Forgejo Actions**: any `.yml`/`.yaml` file inside `.forgejo/workflows/` (and subdirectories)
- **Woodpecker CI**:
  - `.woodpecker.yml` / `.woodpecker.yaml` at the root
  - Any `.yml`/`.yaml` file inside `.woodpecker/` and its subdirectories
- **Dockerfiles**: `Dockerfile`, `Dockerfile.*`, `*.dockerfile`, `*.Dockerfile` (at any depth) — pins `FROM image:tag` lines
- **Docker Compose**: `docker-compose.yml`, `docker-compose.yaml`, `docker-compose.*.yml`, `compose.yml`, `compose.yaml`

## Installation

### One-liner (Linux / macOS)

```sh
curl -fsSL https://raw.githubusercontent.com/Kirskov/Shapin/9363d8f000ec543c33be11fff5b0092b23e9d55d/install.sh | sh
```

The script URL is pinned to a commit SHA so the install script itself cannot be tampered with. Supports Ubuntu, Debian, Kali, Arch, Alpine, Red Hat, Fedora, and macOS. The script will automatically detect your OS and architecture, download the correct binary, and install it to `/usr/local/bin`.

To install a specific version, use the [Manual](#manual) method below.

### Manual

All releases are [immutable](https://docs.github.com/en/repositories/releasing-projects-on-github/managing-releases-in-a-repository#immutable-releases) — the Git tag, commit SHA, and release assets are locked and cannot be modified or deleted after publication.

Download the binary for your platform from the [releases page](https://github.com/Kirskov/Shapin/releases), verify the release attestation, and move it to your PATH:

```sh
# Example for Linux amd64
curl -fsSL https://github.com/Kirskov/Shapin/releases/download/v0.6.3/shapin-linux-amd64 -o shapin
gh attestation verify shapin --repo Kirskov/Shapin
chmod +x shapin
sudo mv shapin /usr/local/bin/
```

### Docker

Images are published to GHCR and available for `linux/amd64` and `linux/arm64`. Always reference by digest, not tag:

```sh
docker run --rm -v $(pwd):/repo ghcr.io/kirskov/shapin@sha256:ee76782a3e71fb6dea2307cba2921929b339bc38baaab47f0027ef0f6028e6e0 # v0.7.7 --path /repo
```

Apply changes (disable dry-run):

```sh
docker run --rm -v $(pwd):/repo ghcr.io/kirskov/shapin@sha256:ee76782a3e71fb6dea2307cba2921929b339bc38baaab47f0027ef0f6028e6e0 # v0.7.7 --path /repo --dry-run=false
```

With API tokens:

```sh
docker run --rm \
  -v $(pwd):/repo \
  -e GITHUB_TOKEN=ghp_xxx \
  -e GITLAB_TOKEN=glpat_xxx \
  ghcr.io/kirskov/shapin@sha256:ee76782a3e71fb6dea2307cba2921929b339bc38baaab47f0027ef0f6028e6e0 # v0.7.7 --path /repo
```

The digest for each release is listed on the [releases page](https://github.com/Kirskov/Shapin/releases). Update the digest when upgrading to a new version.

#### Verify the image signature

Images are signed with [cosign](https://github.com/sigstore/cosign) keyless signing via GitHub Actions OIDC. Verify before running:

```sh
cosign verify \
  --certificate-identity "https://github.com/Kirskov/Shapin/.github/workflows/release.yml@refs/tags/v0.7.7" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  ghcr.io/kirskov/shapin@sha256:ee76782a3e71fb6dea2307cba2921929b339bc38baaab47f0027ef0f6028e6e0 # v0.7.7
```

### Build from source

```sh
git clone https://github.com/Kirskov/Shapin.git
cd Shapin
go build -o shapin .
```

## Usage

```sh
# Dry run — show what would change, write nothing (default)
shapin --path ./myproject

# Apply changes
shapin --path ./myproject --dry-run=false

# Only pin Docker images, leave refs alone
shapin --path ./myproject --pin-refs=false

# Only pin CI refs, leave images alone
shapin --path ./myproject --pin-images=false

# Exclude specific files (comma-separated globs)
shapin --path ./myproject --exclude ".github/workflows/generated.yml,*.skip.yml"

# Use a config file
shapin --config .shapin.json

# With API tokens (required to resolve unpinned action refs)
shapin --path ./myproject --github-token ghp_xxx --gitlab-token glpat_xxx

# Self-hosted GitLab instance
shapin --path ./myproject --gitlab-host https://gitlab.mycompany.com --gitlab-token glpat_xxx
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--path` | `.` | Path to the project to scan |
| `--dry-run` | `true` | Show diff without writing files |
| `--pin-refs` | `true` | Pin `uses:` and `component:` refs to SHAs |
| `--pin-images` | `true` | Pin Docker `image:` tags to digests |
| `--exclude` | — | Comma-separated glob patterns of files to skip |
| `--config` | `.shapin.json` | Path to config file |
| `--github-token` | `$GITHUB_TOKEN` | GitHub API token |
| `--gitlab-token` | `$GITLAB_TOKEN` | GitLab API token |
| `--gitlab-host` | `https://gitlab.com` | GitLab instance URL |
| `--forgejo-host` | `https://codeberg.org` | Forgejo instance URL |
| `--forgejo-token` | `$FORGEJO_TOKEN` | Forgejo API token |

Tokens can also be set via environment variables `GITHUB_TOKEN` and `GITLAB_TOKEN`.

## Config file

All flags can be set in a `.shapin.json` file at the root of your project. CLI flags always take precedence over the config file.

```json
{
  "dry-run": false,
  "pin-refs": true,
  "pin-images": false,
  "github-token": "ghp_...",
  "gitlab-host": "https://gitlab.mycompany.com",
  "exclude": [
    ".github/workflows/generated.yml",
    ".gitlab/auto-*.yml"
  ],
  "tag-mappings": {
    "NODE_TAG": "node",
    "TRIVY_VERSION": "aquasec/trivy",
    "ALPINE_TAG": "alpine"
  }
}
```

## When do you need a token?

| Operation | Token needed? |
|---|---|
| Pinning Docker images | No — uses the public registry API |
| Pinning GitHub Actions `uses:` | Yes — calls the GitHub API to resolve tags |
| Pinning GitLab components | Yes — calls the GitLab API to resolve refs |
| Scanning already-pinned files | No — skipped immediately |

## Rate limiting

API calls to GitHub and GitLab are automatically retried on HTTP 429 (rate limited) or 503 responses. The retry delay is read from the `Retry-After` or `X-RateLimit-Reset` headers, falling back to 60 seconds. Up to 3 retries are attempted before giving up.

## GitLab input pinning

For GitLab CI, image references inside `include[].inputs`, `variables`, and `trigger.include[].inputs` are pinned in two ways:

**1. Auto-detection — key contains `TAG` with `image:tag` value:**

```yaml
variables:
  TRIVY_TAG: aquasec/trivy:0.69.3       # → TRIVY_DIGEST: aquasec/trivy@sha256:... # 0.69.3

include:
  - component: gitlab.com/group/project/scanner@v1
    inputs:
      SCANNER_TAG: myregistry.com/scanner:1.2.3  # → SCANNER_DIGEST: myregistry.com/scanner@sha256:... # 1.2.3
      severity: HIGH                              # skipped — no TAG in key name
```

**2. Mapped keys — plain version values via `tag-mappings` in `.shapin.json`:**

```yaml
# .shapin.json
{
  "tag-mappings": {
    "NODE_VERSION": "node",
    "TRIVY_VERSION": "aquasec/trivy"
  }
}
```

```yaml
inputs:
  NODE_VERSION: '24.13.0'    # → NODE_DIGEST: node@sha256:... # 24.13.0
  TRIVY_VERSION: "0.69.3"   # → TRIVY_DIGEST: aquasec/trivy@sha256:... # 0.69.3
```

In both cases, the key suffix `_TAG` or `_VERSION` is renamed to `_DIGEST`.

## What it can't do

- **Private Docker registries** — only public registries (Docker Hub, GHCR, Quay.io, etc.) are supported
- **`image:` inside a YAML map** — only the simple string form is handled (`image: name:tag`), not `image: { name: ..., tag: ... }`
- **Branch refs** — pinning `uses: action@main` will resolve to the current SHA of `main`, which will become stale over time. Use tags when possible
- **GitLab CI `extends:` or `!reference`** — template includes are not followed
- **CircleCI orbs** — orbs use semver versioning and have no SHA pinning API; only Docker `image:` tags inside CircleCI configs are pinned
- **Bitbucket Pipes** — pipes use semver versioning with no SHA pinning API; only Docker `image:` tags inside Bitbucket Pipelines configs are pinned
- **Woodpecker CI plugins** — Woodpecker plugin steps (`image:`) are pinned, but the Woodpecker plugin registry has no SHA pinning API; only Docker `image:` tags are pinned
