# Shapin

[![Go Reference](https://pkg.go.dev/badge/github.com/Kirskov/Shapin.svg)](https://pkg.go.dev/github.com/Kirskov/Shapin)
[![Go Report Card](https://goreportcard.com/badge/github.com/Kirskov/Shapin)](https://goreportcard.com/report/github.com/Kirskov/Shapin)
[![OpenSSF Baseline](https://www.bestpractices.dev/projects/12470/baseline)](https://www.bestpractices.dev/projects/12470)

Pin floating tags in CI workflow files to immutable SHAs, making your pipelines reproducible and immune to tag mutation attacks.

## Table of contents

- [What it does](#what-it-does)
- [Supported files](#supported-files)
- [Installation](#installation)
  - [One-liner](#one-liner-linux--macos)
  - [Manual](#manual)
  - [Docker](#docker)
  - [Verify release integrity](#verify-release-integrity)
  - [Build from source](#build-from-source)
- [Usage](#usage)
- [Upgrading pinned refs](#upgrading-pinned-refs)
- [Flags](#flags)
- [Output formats](#output-formats)
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
- [Dependencies](#dependencies)
- [Support](#support)
- [Architecture](ARCHITECTURE.md)

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
| GitLab dependency proxy | `image: ${CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX}/node:24.13.0` | `image: node@sha256:cd6fb7... # 24.13.0` |

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
curl -fsSL https://raw.githubusercontent.com/Kirskov/Shapin/df97d9b9fd31e5e9ac80b2257d3eae7d7628509d/install.sh | sh
```

The script URL is pinned to a commit SHA so the install script itself cannot be tampered with. Supports Ubuntu, Debian, Kali, Arch, Alpine, Red Hat, Fedora, and macOS. The script will automatically detect your OS and architecture, download the correct binary, and install it to `/usr/local/bin`.

If you hit GitHub API rate limits (common on shared corporate networks), pass a [personal access token](https://github.com/settings/tokens) with the `public_repo` scope:

```sh
curl -fsSL https://raw.githubusercontent.com/Kirskov/Shapin/df97d9b9fd31e5e9ac80b2257d3eae7d7628509d/install.sh | GITHUB_TOKEN=ghp_xxx sh
```

To install a specific version without an API call:

```sh
curl -fsSL https://raw.githubusercontent.com/Kirskov/Shapin/df97d9b9fd31e5e9ac80b2257d3eae7d7628509d/install.sh | VERSION=v1.3.1 sh
```

### Manual

All releases are [immutable](https://docs.github.com/en/repositories/releasing-projects-on-github/managing-releases-in-a-repository#immutable-releases) — the Git tag, commit SHA, and release assets are locked and cannot be modified or deleted after publication.

Download the binary for your platform from the [releases page](https://github.com/Kirskov/Shapin/releases), verify the release attestation, and move it to your PATH:

```sh
# Example for Linux amd64
curl -fsSL https://github.com/Kirskov/Shapin/releases/download/v1.2.3/shapin-v1.2.3-linux-amd64 -o shapin
gh attestation verify shapin --repo Kirskov/Shapin
chmod +x shapin
sudo mv shapin /usr/local/bin/
```

### Docker

Images are published to GHCR and available for `linux/amd64` and `linux/arm64`. Always reference by digest, not tag:

```sh
docker run --rm -v $(pwd):/repo ghcr.io/kirskov/shapin@sha256:931294ebacbf15d60380a483a28a06881c085e16e4168e8e352e848476f370a0 # v1.2.3 --path /repo
```

Apply changes (disable dry-run):

```sh
docker run --rm -v $(pwd):/repo ghcr.io/kirskov/shapin@sha256:931294ebacbf15d60380a483a28a06881c085e16e4168e8e352e848476f370a0 # v1.2.3 --path /repo --dry-run=false
```

With API tokens:

```sh
docker run --rm \
  -v $(pwd):/repo \
  -e GITHUB_TOKEN=ghp_xxx \
  -e GITLAB_TOKEN=glpat_xxx \
  ghcr.io/kirskov/shapin@sha256:931294ebacbf15d60380a483a28a06881c085e16e4168e8e352e848476f370a0 # v1.2.3 --path /repo
```

The digest for each release is listed on the [releases page](https://github.com/Kirskov/Shapin/releases). Update the digest when upgrading to a new version.

#### Verify the image signature

Images are signed with [cosign](https://github.com/sigstore/cosign) keyless signing via GitHub Actions OIDC. Verify before running:

```sh
cosign verify \
  --certificate-identity "https://github.com/Kirskov/Shapin/.github/workflows/release.yml@refs/tags/v1.2.3" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  ghcr.io/kirskov/shapin@sha256:931294ebacbf15d60380a483a28a06881c085e16e4168e8e352e848476f370a0 # v1.2.3
```

### Verify release integrity

Every release asset can be verified using three independent mechanisms:

**1. Checksum verification** — a `checksums.txt` SHA-256 manifest is included in every release:

```sh
# Download the binary and checksum file
curl -fsSL https://github.com/Kirskov/Shapin/releases/download/v1.2.3/shapin-v1.2.3-linux-amd64 -o shapin
curl -fsSL https://github.com/Kirskov/Shapin/releases/download/v1.2.3/checksums.txt -o checksums.txt

# Verify (expected output: "shapin-v1.2.3-linux-amd64: OK")
sha256sum --ignore-missing -c checksums.txt
```

**2. cosign bundle signature** — each binary is signed with [cosign](https://github.com/sigstore/cosign) keyless signing via the Sigstore transparency log:

```sh
curl -fsSL https://github.com/Kirskov/Shapin/releases/download/v1.2.3/shapin-v1.2.3-linux-amd64.bundle -o shapin.bundle
cosign verify-blob shapin \
  --bundle shapin.bundle \
  --certificate-identity "https://github.com/Kirskov/Shapin/.github/workflows/release.yml@refs/tags/v1.2.3" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com"
# Expected output: Verified OK
```

**3. SLSA provenance attestation** — build provenance is attested via GitHub's attestation framework:

```sh
gh attestation verify shapin --repo Kirskov/Shapin
# Expected output: Attestation verification was successful
```

### Build from source

```sh
git clone https://github.com/Kirskov/Shapin.git
cd Shapin
go build -o shapin ./cmd/shapin
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
| `--output` | — | Write output to a file instead of stdout |
| `--format` | `text` | Output format: `text`, `json`, or `sarif` |

Tokens can also be set via environment variables `GITHUB_TOKEN` and `GITLAB_TOKEN`.

Warnings (drift, branch refs, resolution failures) are always written to stderr, so they never pollute `--output` or piped output.

## Output formats

### JSON

```sh
shapin --path ./myproject --format json --output results.json
```

Outputs a JSON array of file changes, each with the file path and a list of old/new line pairs with their line numbers.

### SARIF

```sh
shapin --path ./myproject --format sarif --output results.sarif
```

Outputs [SARIF 2.1.0](https://docs.oasis-open.org/sarif/sarif/v2.1.0/sarif-v2.1.0.html) for upload to GitHub Code Scanning. Each result includes the file path and exact line number, so annotations appear inline in pull requests.

Upload example:

```yaml
- name: Run Shapin
  run: shapin --path . --format sarif --output shapin.sarif --dry-run=false

- name: Upload SARIF
  uses: github/codeql-action/upload-sarif@v3
  with:
    sarif_file: shapin.sarif
```

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

**Branch ref warning:**
If a ref points to a well-known branch name (`main`, `master`, `develop`, `development`) or a common branch prefix (`feat/`, `fix/`, `bug/`, `hotfix/`, `feature/`, `bugfix/`, `release/`), a red warning is printed — the pinned SHA will become stale as the branch moves forward. Use a tag instead.

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

**Branch ref warning:**
Same as GitHub Actions — if the component ref is a well-known branch name, a red warning is printed.

#### Version inputs

Two patterns are detected at any nesting level across the entire file:

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
| `DOCKER`, `DIND` | `docker` |
| `KANIKO` | `gcr.io/kaniko-project/executor` |
| `GRADLE` | `gradle` |
| `MAVEN`, `MVN` | `maven` |
| `PHP` | `php` |
| `ELASTICSEARCH`, `ES` | `elasticsearch` |
| `MONGO`, `MONGODB` | `mongo` |
| `RABBITMQ` | `rabbitmq` |
| `GRYPE` | `anchore/grype` |
| `SEMGREP` | `semgrep/semgrep` |
| `COSIGN` | `cgr.dev/chainguard/cosign` |
| `ANSIBLE` | `cytopia/ansible` |
| `PACKER` | `hashicorp/packer` |
| `VAULT` | `hashicorp/vault` |
| `GOLANGCI`, `GOLANGCI_LINT` | `golangci/golangci-lint` |
| `OPENTOFU`, `TOFU` | `ghcr.io/opentofu/opentofu` |
| `VALKEY` | `valkey/valkey` |
| `GRAFANA` | `grafana/grafana` |
| `PROMETHEUS` | `prom/prometheus` |
| `ALERTMANAGER` | `prom/alertmanager` |
| `TRAEFIK` | `traefik` |
| `CADDY` | `caddy` |
| `TELEGRAF` | `telegraf` |
| `BASH` | `bash` |
| `SELENIUM` | `selenium/standalone-chrome` |
| `SYFT` | `anchore/syft` |

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

#### Dependency proxy

Images pulled through the [GitLab Dependency Proxy](https://docs.gitlab.com/ee/user/packages/dependency_proxy/) use a CI variable as their registry prefix:

```yaml
image: ${CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX}/node:24.13.0-alpine3.23
image: ${CI_DEPENDENCY_PROXY_DIRECT_GROUP_IMAGE_PREFIX}/alpine:3.20
```

Shapin automatically strips the proxy prefix and resolves the underlying Docker Hub image to a digest:

```yaml
image: node@sha256:cd6fb7... # 24.13.0-alpine3.23
image: alpine@sha256:... # 3.20
```

Both `${VAR}/` and `$VAR/` syntaxes are supported for `CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX` and `CI_DEPENDENCY_PROXY_DIRECT_GROUP_IMAGE_PREFIX`.

> **Note:** The GitLab Dependency Proxy only supports Docker Hub images. Shapin resolves the stripped image name against Docker Hub, which matches what the proxy itself does.

**Limitations:**
- `extends:` and `!reference` template includes are not followed

---

### Forgejo Actions

Pins `uses: owner/repo@tag` refs to their commit SHA. Falls back to `code.forgejo.org` for community actions.

```yaml
- uses: actions/checkout@v1
# → - uses: actions/checkout@abc1234... # v1
```

**Branch ref warning:**
Same as GitHub Actions — a red warning is printed if the ref is a well-known branch name.

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

## Dependencies

Shapin has minimal runtime dependencies, all managed via Go modules.

**Selection** — dependencies are chosen to be small, well-maintained, and auditable. The full dependency list with pinned versions and checksums is declared in [`go.mod`](go.mod) and [`go.sum`](go.sum).

**Obtaining** — dependencies are fetched by the Go toolchain (`go mod download`) during development and CI builds. All checksums are verified against `go.sum` and the [Go checksum database](https://sum.golang.org) on every build.

**Tracking** — [Dependabot](https://github.com/Kirskov/Shapin/blob/main/.github/dependabot.yml) is configured to open weekly pull requests for outdated Go module and GitHub Actions dependencies. Security advisories are tracked via GitHub's dependency graph and the `Vulnerabilities` OpenSSF Scorecard check.

## Support

Only the **latest release** is actively supported. When a new version is published, the previous release is no longer maintained.

| Type | Included |
|---|---|
| Security vulnerability fixes | Yes — latest release only |
| Bug fixes | Yes — latest release only |
| Backports to older releases | No |

**A release stops receiving security updates as soon as a newer version is published.** Users should upgrade to the latest release to remain protected.

For bug reports open a [GitHub Issue](https://github.com/Kirskov/Shapin/issues). For security vulnerabilities follow the [private disclosure process](SECURITY.md). There is no formal LTS program — upgrading is straightforward as Shapin is a single self-contained binary.
