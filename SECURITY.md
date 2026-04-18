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

## SAST Remediation Policy

Shapin runs CodeQL and gosec on every push to `main` and every pull request. Findings are uploaded to GitHub Code Scanning as SARIF.

### Thresholds

| Severity | Remediation threshold |
|---|---|
| Critical / High | Must be fixed before merging — no exceptions |
| Medium | Must be fixed or documented as a false positive within 30 days |
| Low / Informational | Assessed on a case-by-case basis |

### Process

1. SAST tools run automatically via the `codeql` and `gosec` jobs in `ci.yml` on every push and PR
2. Findings appear inline in pull requests and in the GitHub Code Scanning tab
3. Critical and High findings must be resolved before a PR is merged
4. False positives must be dismissed in GitHub Code Scanning with a documented reason — they are never silently ignored
5. Any finding that cannot be immediately fixed must be tracked as a GitHub issue with an agreed remediation deadline

## SCA Remediation Policy

Shapin uses Grype to scan the SBOM on every CI run. The following thresholds apply:

### Vulnerabilities

| Severity | Remediation threshold |
|---|---|
| Critical | Fix before the next release — no exceptions |
| High | Fix within the next release cycle |
| Medium | Fix within 90 days or document as not affected in `vex.json` |
| Low / Negligible | Assessed on a case-by-case basis; documented in `vex.json` if not applicable |

**Process:**
1. Grype findings are uploaded as SARIF to GitHub Code Scanning on every push to `main`
2. A dedicated `sca-gate` job runs Grype against the SBOM before every release — Critical and High findings fail the build, blocking the `release` and `docker` jobs from running
3. To unblock a release, the maintainer must either upgrade the affected dependency or add a justified `not_affected` entry in `vex.json` with an impact statement per the OpenVEX spec
4. The `vex.json` file is passed to Grype during the gate check so documented non-applicable findings do not block the release

### Licenses

All dependencies must use OSI-approved open source licenses compatible with the MIT license. Dependencies with unknown, proprietary, or copyleft (GPL) licenses are not permitted without prior review.

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

### Secure design principles (Saltzer & Schroeder)

| Principle | How it is applied |
|---|---|
| **Economy of mechanism** | Shapin has a small, focused codebase — read files, call APIs, rewrite files. No plugin system, no scripting engine, no daemon. |
| **Fail-safe defaults** | `--dry-run` defaults to `true` — no files are modified unless the user explicitly opts in. Missing API responses leave the file unchanged. |
| **Complete mediation** | Every file path is validated against the project root via `assertWithinRoot` before any read or write. |
| **Open design** | The tool is fully open source. Security properties do not rely on secrecy of the implementation. |
| **Separation of privilege** | API tokens are scoped per provider (GitHub, GitLab, Forgejo). No single credential grants access to all providers. |
| **Least common mechanism** | Providers share no mutable global state. Each provider is an independent struct with its own HTTP client. |
| **Psychological acceptability** | The CLI has sensible defaults and clear flag names. Safe operation (dry-run) is the default; destructive operation (writing files) requires an explicit opt-in. |

### Common implementation weaknesses

| Weakness (CWE / OWASP) | Mitigation |
|---|---|
| **CWE-22 Path traversal** | `assertWithinRoot` validates every path before read/write; covered by gosec and CodeQL. |
| **CWE-20 Improper input validation** | `--format` validated against an explicit allowlist; host URLs must start with `https://`; file paths validated as above. |
| **CWE-400 ReDoS** | Go's `regexp` uses a linear-time RE2 engine — catastrophic backtracking is impossible by design. |
| **CWE-312 Cleartext storage of sensitive data** | Tokens are never written to output, diffs, or SARIF; gosec scans for hardcoded credentials on every commit. |
| **CWE-295 Improper certificate validation** | Go's `net/http` validates TLS certificates by default; no `InsecureSkipVerify` is used anywhere in the codebase. |
| **CWE-326 Inadequate encryption strength** | All outbound API calls use HTTPS. Go 1.22+ defaults to TLS 1.2+ with AEAD-only cipher suites. |
| **OWASP A06 Vulnerable components** | Grype scans the SBOM on every CI run; Critical/High CVEs block the release pipeline. |

### Out of scope

- Private registry credential storage — users are responsible for secure token handling.
- Correctness of upstream APIs — Shapin trusts that registries and VCS APIs return accurate data.
- Continuous post-pin monitoring — drift detection covers tag movement, not SHA-level tampering after pinning.
