# Security Policy

## Supported Versions

Only the latest release receives security fixes.

## Reporting a Vulnerability

Please **do not** open a public issue for security vulnerabilities.

Report them privately using one of the following methods:

- **GitHub private disclosure**: use the [Report a vulnerability](https://github.com/Kirskov/Shapin/security/advisories/new) button on the Security tab
- **Email**: security.fountain045@passmail.net

Include:

- A description of the vulnerability
- Steps to reproduce
- Potential impact

You will receive a response within 7 days. If the report is confirmed, a fix will be released as soon as possible and you will be credited in the release notes (unless you prefer to remain anonymous).

## Secrets and Credentials Policy

Shapin does not store, manage, or have custody of any secrets or credentials. API tokens are accepted at runtime via CLI flags or environment variables and are never persisted by the tool.

### Project repository

- No secrets are committed to the repository or its history
- The CI/CD pipeline uses only the ephemeral `GITHUB_TOKEN` provisioned automatically by GitHub per workflow run, with per-job minimum permissions
- gosec and CodeQL scan for hardcoded credentials on every commit

### User responsibilities

Users who pass tokens to Shapin (`--github-token`, `--gitlab-token`, `--forgejo-token`) are responsible for their secure handling:

- Pass tokens via environment variables sourced from your secrets store, not as hardcoded values
- Do not commit `.shapin.json` if it contains token values
- Rotate tokens immediately if accidental exposure is suspected

## Vulnerability Disclosure

Once a vulnerability is confirmed and a fix is released, the project will publicly disclose the details through the following channels:

- **GitHub Security Advisory**: a published advisory on the [Security Advisories](https://github.com/Kirskov/Shapin/security/advisories) page, including affected versions, a description of the vulnerability, and upgrade instructions
- **Release notes**: the fix release changelog will reference the advisory
- **CVE**: a CVE identifier will be requested via GitHub for significant vulnerabilities

Disclosures include:
- Affected version(s)
- How to determine if you are vulnerable
- Mitigation or remediation instructions (typically: upgrade to the latest release)

## Vulnerability Disclosure

Once a vulnerability is confirmed and a fix is released, the project will publicly disclose the details through:

- **GitHub Security Advisory**: a published advisory on the [Security Advisories](https://github.com/Kirskov/Shapin/security/advisories) page, including affected versions, a description of the vulnerability, and upgrade instructions
- **Release notes**: the fix release changelog will reference the advisory

Disclosures include:
- Affected version(s)
- How to determine if you are vulnerable
- Mitigation or remediation instructions (typically: upgrade to the latest release)

## Security Assessment

### Scope

Shapin reads CI/CD configuration files from a local directory, queries upstream APIs (GitHub, GitLab, Forgejo, Docker registries) to resolve tags to immutable SHAs, and rewrites the files in place. It runs on developer workstations and in CI/CD pipelines.

### Trust boundaries

| Boundary | Description |
|---|---|
| Local filesystem | Files read and written are within the user-specified `--path` root |
| GitHub API | Resolves `uses:` action refs — response integrity not cryptographically verified |
| GitLab API | Resolves component refs and version variables — response integrity not cryptographically verified |
| Forgejo API | Resolves action refs — response integrity not cryptographically verified |
| Docker Registry API | Resolves image tags to digests — digest is content-addressed (SHA-256) |
| Environment / config file | Tokens read from environment variables or `.shapin.json` |

### Threat model

**T1 — Compromised upstream API** (high impact, low likelihood)

A compromised GitHub, GitLab, Forgejo, or Docker registry API could return a malicious SHA. Docker digests are content-addressed (SHA-256) so the fetched image is immutable once recorded. Git SHAs from VCS APIs are not content-addressed in the same way. The drift detection feature warns when a previously pinned SHA no longer matches the current tag, surfacing tampering post-hoc.

**T2 — Directory traversal** (high impact, very low likelihood)

A malicious `--path` argument or symlink could cause reads or writes outside the intended root. Mitigated by `assertWithinRoot`, which validates every path before use. Covered by gosec and CodeQL on every commit.

**T3 — Token leakage** (high impact, low likelihood)

API tokens passed via flags or `.shapin.json` could be leaked via logs or output. Tokens are never included in error messages, diffs, or SARIF output. In CI, tokens should be passed via environment variables sourced from secrets. gosec scans for hardcoded credentials on every commit.

**T4 — Regex denial of service** (low impact, very low likelihood)

Go's `regexp` package uses a linear-time RE2 engine — catastrophic backtracking is not possible by design.

**T5 — Malicious config file** (medium impact, low likelihood)

A malicious `.shapin.json` could supply attacker-controlled tokens or exclude files from pinning. CLI flags always take precedence. No config values are executed as shell commands.

**T6 — Supply chain compromise of Shapin itself** (high impact, very low likelihood)

Mitigated by cosign-signed release binaries, SLSA provenance attestations, published `checksums.txt`, SHA-pinned CI actions, and a pinned install script.

### Out of scope

- Private registry credential storage — users are responsible for secure token handling.
- Correctness of upstream APIs — Shapin trusts that registries and VCS APIs return accurate data.
- Continuous post-pin monitoring — drift detection covers tag movement, not SHA-level tampering after pinning.
