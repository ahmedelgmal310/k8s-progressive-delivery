# Security Policy

## Supported versions

Only the latest image published from `main` (`ghcr.io/ahmedelgmal310/demo-api:sha-*`) receives fixes.

## Reporting a vulnerability

Please report vulnerabilities privately through
[GitHub Security Advisories](https://github.com/ahmedelgmal310/k8s-progressive-delivery/security/advisories/new).
Do not open a public issue. You can expect an acknowledgement within 72 hours.

## Supply-chain controls in this repository

- Every GitHub Action is pinned to a full commit SHA; base images are pinned by digest.
- Images are distroless, run as a non-root user with a read-only root filesystem.
- CI gates: `govulncheck`, Trivy (HIGH/CRITICAL, fixed vulnerabilities fail the build), Gitleaks, hadolint.
- Published images carry SBOM and SLSA provenance attestations and are signed keyless with cosign.
  Verify with:

  ```sh
  cosign verify ghcr.io/ahmedelgmal310/demo-api:<tag> \
    --certificate-identity-regexp '^https://github.com/ahmedelgmal310/k8s-progressive-delivery/.github/workflows/release.yml@refs/heads/main$' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
  ```
