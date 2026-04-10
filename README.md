# Shapin

Pin floating tags in CI workflow files to immutable SHAs, making your pipelines reproducible and immune to tag mutation attacks.

## Table of contents

- [What it does](#what-it-does)
- [Supported files](#supported-files)
- [Installation](#installation)
  - [One-liner](#one-liner-linux--macos)
  - [Manual](#manual)
  - [Docker](#docker)
  - [Build from source](#build-from-source)
- [Usage](#usage)
- [Upgrading pinned refs](#upgrading-pinned-refs)
- [Flags](#flags)
- [Config file](#config-file)
- [Providers](#providers)
  - [GitHub Actions](#github-actions)
  - [GitLab CI](#gitlab-ci)
  - [Forgejo Actions](#forgejo-actions)
  - [CircleCI](#circleci)
  - [Bitbucket Pipelines](#bitbucket-pipelines)
  - [Woodpecker CI](#woodpecker-ci)
  - [Dockerfile](#dockerfile)
  - [Docker Compose](#docker-compose)
- [When do you need a token?](#when-do-you-need-a-token)
- [Rate limiting](#rate-limiting)
- [What it can't do](#what-it-cant-do)

## What it does

| Reference type | Before | After |
|---|---|---|
| GitHub Action | `uses: actions/checkout@v4` | `uses: actions/checkout@abc1234... # v4` |
| Forgejo Action | `uses: actions/checkout@v1` | `uses: actions/checkout@abc1234... # v1` |
| Docker image (`image:`) | `image: maildev/maildev:2.2.1` | `image: maildev/maildev@sha256:180ef5... # 2.2.1` |
| Dockerfile `FROM` | `FROM golang:1.24-alpine AS builder` | `FROM golang@sha256:8bee19... # 1.24-alpine AS builder` |
| GitLab component ref | `component: gitlab.com/group/proj/name@v1.0.0` | `component: gitlab.com/group/proj/name@abc1234... # v1.0.0` |
| GitLab `image:tag` variable | `TRIVY_TAG: aquasec/trivy:0.69.3` | `TRIVY_TAG: aquasec/trivy@sha256:eafae... # 0.69.3` |
| GitLab bare version variable | `TF_VERSION: "1.14.8"` | `TF_DIGEST: "sha256:6bbb82... # 1.14.8"` |
| GitLab trigger input | `TF_VERSION: "1.14.8"` (under `inputs:`) | `TF_DIGEST: "sha256:6bbb82... # 1.14.8"` |

Already-pinned refs and digests are left untouched. Every provider checks pinned SHAs against their current tag — a warning is printed if the tag has been moved to a different commit (drift detection).

## Supported files

The tool scans recursively under `--path`, skipping `node_modules`, `.git`, `vendor`, and `dist`.

- **GitHub Actions**: any `.yml`/`.yaml` file inside `.github/workflows/` (and subdirectories)
- **GitLab CI**:
  - `.gitlab-ci.yml` / `.gitlab-ci.yaml` / `.gitlab-ci-*.yml` at any depth (supports monorepos where each subdirectory is its own project)
  - Any `.yml`/`.yaml` file inside `.gitlab/` and its subdirectories, at any depth
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
curl -fsSL https://raw.githubusercontent.com/Kirskov/Shapin/18d784ba2b0ea6009c4abcce82afee49e09a554f/install.sh | sh
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

## Upgrading pinned refs

To upgrade a pinned ref to a newer version, update it and rerun `shapin`.

**Action / component refs** — change the SHA back to the new tag:

```yaml
# before (pinned)
- uses: actions/checkout@abc1234... # v4
# edit to
- uses: actions/checkout@v5
```

**Version variables** — just set the new version directly in the `_DIGEST` key:

```yaml
# before (pinned)
TF_DIGEST: "sha256:6bbb82... # 1.14.8"
# edit to
TF_DIGEST: "1.15.0"
```

Then rerun `shapin --path .` to resolve the new digest.

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
  ]
}
```

## Providers

### GitHub Actions

Pins `uses: owner/repo@tag` refs to their commit SHA. Requires `--github-token` to call the GitHub API.

```yaml
- uses: actions/checkout@v4
# → - uses: actions/checkout@abc1234... # v4
```

Already-pinned refs (`@sha # tag`) are checked for drift — a warning is printed if the tag has been moved to a different commit.

**Limitations:**
- Branch refs (e.g. `@main`) are resolved to the current HEAD SHA, which will become stale over time — use tags when possible

---

### GitLab CI

Scans `.gitlab-ci.yml`, `.gitlab-ci.yaml`, `.gitlab-ci-*.yml`, and any `.yml`/`.yaml` inside `.gitlab/` — at any directory depth, supporting monorepos where each subdirectory is its own project.

#### Component refs

Component refs (`component: path@tag`) are pinned to their commit SHA using the GitLab tags API — no token required for public components.

```yaml
include:
  - component: gitlab.com/my-group/my-catalogue/deploy@2.1.4
    # → component: gitlab.com/my-group/my-catalogue/deploy@abc1234... # 2.1.4
```

The predefined variables `$CI_SERVER_FQDN` and `$CI_SERVER_HOST` are automatically substituted with `--gitlab-host` (default: `gitlab.com`):

```yaml
component: $CI_SERVER_FQDN/components/sast/sast@3.4.0
# → component: $CI_SERVER_FQDN/components/sast/sast@0a29cf... # 3.4.0
```

Other `$VARIABLE` prefixes (e.g. `$SPLIT_GLOBAL_COMPONENT_ROOT`) cannot be resolved and are left untouched.

For **private components**, pass `--gitlab-token`. Without it a warning is printed:
```
warn: GitLab component .../private-comp@v1.0.0: HTTP 404 — try --gitlab-token if this is a private component
```

#### Version inputs

Two patterns are detected inside `variables:` and `inputs:` blocks at any nesting level:

**1. `image:tag` values** — keys containing `TAG` with a full `image:tag` value:

```yaml
SCANNER_TAG: myregistry.com/custom-scanner:1.2.3
# → SCANNER_TAG: myregistry.com/custom-scanner@sha256:... # 1.2.3
```

**2. Bare version values** — keys ending or starting with `_VERSION`, `_TAG`, or `_DIGEST` whose stem matches a built-in or user-supplied image mapping. The key is renamed to use `_DIGEST`:

```yaml
TF_VERSION: '1.13.5'     # → TF_DIGEST: 'sha256:...' # 1.13.5
VERSION_TF: '1.13.5'     # → DIGEST_TF: 'sha256:...' # 1.13.5
```

Values starting with `$` (CI variable interpolation) or already containing a digest are left untouched.

#### Built-in stem mappings

The stem is the key name with `_VERSION`, `_TAG`, or `_DIGEST` stripped (prefix or suffix):

| Stem(s) | Docker image |
|---|---|
| `TF`, `TERRAFORM` | `hashicorp/terraform` |
| `NODE`, `NODEJS` | `node` |
| `TRIVY` | `aquasec/trivy` |
| `JAVA` | `eclipse-temurin` |
| `ALPINE` | `alpine` |
| `PYTHON` | `python` |
| `GO`, `GOLANG` | `golang` |
| `RUBY` | `ruby` |
| `RUST` | `rust` |
| `DOTNET` | `mcr.microsoft.com/dotnet/sdk` |
| `KUBECTL` | `bitnami/kubectl` |
| `HELM` | `alpine/helm` |
| `POSTGRES` | `postgres` |
| `MYSQL` | `mysql` |
| `REDIS` | `redis` |
| `NGINX` | `nginx` |
| `SONAR`, `SONARQUBE` | `sonarsource/sonar-scanner-cli` |
| `AWS_CLI`, `AWSCLI` | `amazon/aws-cli` |
| `CURL` | `curlimages/curl` |
| `GIT_CLIFF` | `orhunp/git-cliff` |

For images not in this list, add a `tag-mappings` entry to `.shapin.json`:

```json
{
  "tag-mappings": {
    "MYAPP": "registry.internal/myapp",
    "TF": "myregistry.internal/mirror/terraform"
  }
}
```

User-supplied mappings override the built-ins.

**Limitations:**
- `extends:` and `!reference` template includes are not followed

---

### Forgejo Actions

Pins `uses: owner/repo@tag` refs to their commit SHA. Falls back to `code.forgejo.org` for community actions.

```yaml
- uses: actions/checkout@v1
# → - uses: actions/checkout@abc1234... # v1
```

---

### CircleCI

Pins Docker `image:` tags inside `.circleci/config.yml` and `.circleci/config.yaml` to digests.

**Limitations:**
- CircleCI orbs use semver versioning with no SHA pinning API — only `image:` tags are pinned

---

### Bitbucket Pipelines

Pins Docker `image:` tags inside `bitbucket-pipelines.yml` and `bitbucket-pipelines.yaml` to digests.

**Limitations:**
- Bitbucket Pipes use semver versioning with no SHA pinning API — only `image:` tags are pinned

---

### Woodpecker CI

Pins Docker `image:` tags inside `.woodpecker.yml`, `.woodpecker.yaml`, and any `.yml`/`.yaml` inside `.woodpecker/`.

**Limitations:**
- Woodpecker plugin steps are pinned by Docker image digest, but there is no SHA pinning API for the plugin registry itself

---

### Dockerfile

Pins `FROM image:tag` lines to digests at any depth. The `AS alias` is preserved.

```dockerfile
FROM golang:1.24-alpine AS builder
# → FROM golang@sha256:... # 1.24-alpine AS builder
```

`FROM scratch` is left untouched.

---

### Docker Compose

Pins `image:` tags in `docker-compose.yml`, `docker-compose.yaml`, `docker-compose.*.yml`, `compose.yml`, and `compose.yaml` files at any depth.

---

## When do you need a token?

| Operation | Token needed? |
|---|---|
| Pinning Docker images | No — uses the public registry API |
| Pinning GitHub Actions `uses:` | Yes — `--github-token` |
| Pinning GitLab components (public) | No — uses the public GitLab API |
| Pinning GitLab components (private) | Yes — `--gitlab-token` |
| Pinning Forgejo actions | No for public, `--forgejo-token` for private |
| Scanning already-pinned files | No — skipped immediately |

## Rate limiting

API calls are automatically retried on HTTP 429 (rate limited) or 503 responses. The retry delay is read from the `Retry-After` or `X-RateLimit-Reset` headers, falling back to 60 seconds. Up to 3 retries are attempted before giving up.

## What it can't do

- **Private Docker registries** — only public registries (Docker Hub, GHCR, Quay.io, etc.) are supported
- **`image:` inside a YAML map** — only the simple string form is handled (`image: name:tag`), not `image: { name: ..., tag: ... }`
- **Branch refs** — pinning `@main` resolves to the current HEAD SHA, which will become stale — use tags when possible
- **Unknown GitLab CI variable prefixes** — component paths starting with `$SPLIT_GLOBAL_COMPONENT_ROOT` or similar custom variables cannot be resolved
