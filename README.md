# pintosha

Pin floating tags in CI workflow files to immutable SHAs, making your pipelines reproducible and immune to tag mutation attacks.

## What it does

| Reference type | Before | After |
|---|---|---|
| GitHub Action | `uses: actions/checkout@v4` | `uses: actions/checkout@abc1234... # v4` |
| Docker image | `image: maildev/maildev:2.2.1` | `image: maildev/maildev@sha256:180ef5... # 2.2.1` |
| GitLab component | `component: gitlab.com/group/project/comp@v1` | `component: gitlab.com/group/project/comp@abc1234... # v1` |

Already-pinned refs (SHA or digest) are left untouched.

## Supported files

The tool scans recursively under `--path`, skipping `node_modules`, `.git`, `vendor`, and `dist`.

- **GitHub Actions**: any `.yml`/`.yaml` file inside `.github/workflows/` (and subdirectories)
- **GitLab CI**:
  - `.gitlab-ci.yml` / `.gitlab-ci.yaml` / `.gitlab-ci-*.yml` at the root
  - Any `.yml`/`.yaml` file inside `.gitlab/` and its subdirectories

## Installation

### One-liner (Linux / macOS)

```sh
curl -fsSL https://raw.githubusercontent.com/Kirskov/Digestify-My-Ci/main/install.sh | sh
```

Supports Ubuntu, Debian, Kali, Arch, Alpine, Red Hat, Fedora, and macOS.
The script will automatically detect your OS and architecture, download the correct binary, verify its SHA256 checksum, and install it to `/usr/local/bin`.

To install a specific version:

```sh
VERSION=v0.1.0 curl -fsSL https://raw.githubusercontent.com/Kirskov/Digestify-My-Ci/main/install.sh | sh
```

### Manual

Download the binary for your platform from the [releases page](https://github.com/Kirskov/Digestify-My-Ci/releases), verify the checksum against `checksums.txt`, and move it to your PATH:

```sh
# Example for Linux amd64
curl -fsSL https://github.com/Kirskov/Digestify-My-Ci/releases/latest/download/digestify-my-ci-linux-amd64 -o digestify-my-ci
curl -fsSL https://github.com/Kirskov/Digestify-My-Ci/releases/latest/download/checksums.txt -o checksums.txt
grep digestify-my-ci-linux-amd64 checksums.txt | sha256sum --check
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
# Dry run — show what would change, write nothing
pintosha --path ./myproject --dry-run

# Apply changes
pintosha --path ./myproject

# Only pin Docker images, leave action refs alone
pintosha --path ./myproject --pin-actions=false

# Only pin GitHub/GitLab action refs, leave images alone
pintosha --path ./myproject --pin-images=false

# With API tokens (required to resolve unpinned action refs)
pintosha --path ./myproject --github-token ghp_xxx --gitlab-token glpat_xxx

# Self-hosted GitLab instance
pintosha --path ./myproject --gitlab-host https://gitlab.mycompany.com --gitlab-token glpat_xxx
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--path` | `.` | Path to the project to scan |
| `--dry-run` | `false` | Show diff without writing files |
| `--pin-actions` | `true` | Pin `uses:` and `component:` refs to SHAs |
| `--pin-images` | `true` | Pin Docker `image:` tags to digests |
| `--github-token` | `$GITHUB_TOKEN` | GitHub API token |
| `--gitlab-token` | `$GITLAB_TOKEN` | GitLab API token |
| `--gitlab-host` | `https://gitlab.com` | GitLab instance URL |

Tokens can also be set via environment variables `GITHUB_TOKEN` and `GITLAB_TOKEN`.

## When do you need a token?

| Operation | Token needed? |
|---|---|
| Pinning Docker images | No — uses the public registry API |
| Pinning GitHub Actions `uses:` | Yes — calls the GitHub API to resolve tags |
| Pinning GitLab components | Yes — calls the GitLab API to resolve refs |
| Scanning already-pinned files | No — skipped immediately |

## What it can't do

- **Private Docker registries** — only public registries (Docker Hub, GHCR, Quay.io, etc.) are supported
- **`image:` inside a YAML map** — only the simple string form is handled (`image: name:tag`), not `image: { name: ..., tag: ... }`
- **Branch refs** — pinning `uses: action@main` will resolve to the current SHA of `main`, which will become stale over time. Use tags when possible
- **GitLab CI `extends:` or `!reference`** — template includes are not followed
- **Monorepos with many workflow files** — all matching files are processed, but there is no filtering by file name yet
