# pintosha

Pin floating tags in CI workflow files to immutable SHAs, making your pipelines reproducible and immune to tag mutation attacks.

## What it does

| Reference type | Before | After |
|---|---|---|
| GitHub Action | `uses: actions/checkout@v4` | `uses: actions/checkout@abc1234... # v4` |
| Docker image | `image: maildev/maildev:2.2.1` | `image: maildev/maildev@sha256:180ef5... # 2.2.1` |
| GitLab component | `component: gitlab.com/group/project/comp@v1` | `component: gitlab.com/group/project/comp@abc1234... # v1` |
| GitLab TAG input | `TRIVY_TAG: aquasec/trivy:0.48.0` | `TRIVY_TAG: aquasec/trivy@sha256:eafae... # 0.48.0` |
| GitLab TAG variable | `TRIVY_TAG: aquasec/trivy:0.48.0` | `TRIVY_TAG: aquasec/trivy@sha256:eafae... # 0.48.0` |

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

## Installation

### One-liner (Linux / macOS)

```sh
curl -fsSL https://raw.githubusercontent.com/Kirskov/Digestify-My-Ci/9363d8f000ec543c33be11fff5b0092b23e9d55d/install.sh | sh
```

The script URL is pinned to a commit SHA so the install script itself cannot be tampered with. Supports Ubuntu, Debian, Kali, Arch, Alpine, Red Hat, Fedora, and macOS. The script will automatically detect your OS and architecture, download the correct binary, and install it to `/usr/local/bin`.

To install a specific version, use the [Manual](#manual) method below.

### Manual

All releases are [immutable](https://docs.github.com/en/repositories/releasing-projects-on-github/managing-releases-in-a-repository#immutable-releases) — the Git tag, commit SHA, and release assets are locked and cannot be modified or deleted after publication.

Download the binary for your platform from the [releases page](https://github.com/Kirskov/Digestify-My-Ci/releases), verify the release attestation, and move it to your PATH:

```sh
# Example for Linux amd64
curl -fsSL https://github.com/Kirskov/Digestify-My-Ci/releases/download/v0.6.3/digestify-my-ci-linux-amd64 -o digestify-my-ci
gh attestation verify digestify-my-ci --repo Kirskov/Digestify-My-Ci
chmod +x digestify-my-ci
sudo mv digestify-my-ci /usr/local/bin/
```

### Build from source

```sh
git clone https://github.com/Kirskov/Digestify-My-Ci.git
cd Digestify-My-Ci
go build -o digestify-my-ci .
```

## Usage

```sh
# Dry run — show what would change, write nothing (default)
digestify-my-ci --path ./myproject

# Apply changes
digestify-my-ci --path ./myproject --dry-run=false

# Only pin Docker images, leave action refs alone
digestify-my-ci --path ./myproject --pin-actions=false

# Only pin GitHub/GitLab action refs, leave images alone
digestify-my-ci --path ./myproject --pin-images=false

# Exclude specific files (comma-separated globs)
digestify-my-ci --path ./myproject --exclude ".github/workflows/generated.yml,*.skip.yml"

# Use a config file
digestify-my-ci --config .digestify.json

# With API tokens (required to resolve unpinned action refs)
digestify-my-ci --path ./myproject --github-token ghp_xxx --gitlab-token glpat_xxx

# Self-hosted GitLab instance
digestify-my-ci --path ./myproject --gitlab-host https://gitlab.mycompany.com --gitlab-token glpat_xxx
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--path` | `.` | Path to the project to scan |
| `--dry-run` | `true` | Show diff without writing files |
| `--pin-actions` | `true` | Pin `uses:` and `component:` refs to SHAs |
| `--pin-images` | `true` | Pin Docker `image:` tags to digests |
| `--exclude` | — | Comma-separated glob patterns of files to skip |
| `--config` | `.digestify.json` | Path to config file |
| `--github-token` | `$GITHUB_TOKEN` | GitHub API token |
| `--gitlab-token` | `$GITLAB_TOKEN` | GitLab API token |
| `--gitlab-host` | `https://gitlab.com` | GitLab instance URL |
| `--forgejo-host` | `https://codeberg.org` | Forgejo instance URL |
| `--forgejo-token` | `$FORGEJO_TOKEN` | Forgejo API token |

Tokens can also be set via environment variables `GITHUB_TOKEN` and `GITLAB_TOKEN`.

## Config file

All flags can be set in a `.digestify.json` file at the root of your project. CLI flags always take precedence over the config file.

```json
{
  "dry-run": false,
  "pin-actions": true,
  "pin-images": false,
  "github-token": "ghp_...",
  "gitlab-host": "https://gitlab.mycompany.com",
  "exclude": [
    ".github/workflows/generated.yml",
    ".gitlab/auto-*.yml"
  ]
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

## GitLab TAG convention

For GitLab CI, image references inside `include[].inputs` and top-level `variables` are pinned when the key name contains `TAG` (case-insensitive). This convention avoids false positives on arbitrary string inputs.

```yaml
# Pinned — key contains TAG
variables:
  TRIVY_TAG: aquasec/trivy:0.69.3       # → aquasec/trivy@sha256:... # 0.69.3

include:
  - component: gitlab.com/group/project/scanner@v1
    inputs:
      SCANNER_TAG: myregistry.com/scanner:1.2.3  # → myregistry.com/scanner@sha256:... # 1.2.3
      severity: HIGH                              # skipped — no TAG in key name
```

## What it can't do

- **Private Docker registries** — only public registries (Docker Hub, GHCR, Quay.io, etc.) are supported
- **`image:` inside a YAML map** — only the simple string form is handled (`image: name:tag`), not `image: { name: ..., tag: ... }`
- **Branch refs** — pinning `uses: action@main` will resolve to the current SHA of `main`, which will become stale over time. Use tags when possible
- **GitLab CI `extends:` or `!reference`** — template includes are not followed
- **CircleCI orbs** — orbs use semver versioning and have no SHA pinning API; only Docker `image:` tags inside CircleCI configs are pinned
- **Bitbucket Pipes** — pipes use semver versioning with no SHA pinning API; only Docker `image:` tags inside Bitbucket Pipelines configs are pinned
