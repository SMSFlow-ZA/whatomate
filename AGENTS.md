# Managed Whatomate fork

This public AGPL fork owns application-side plan enforcement. Keep the upstream
license and corresponding source available for every deployed modified release.

- Do not copy private billing, provisioning, deployment state, credentials,
  customer data or internal integration notes into this repository.
- Feature branches use `codex/`. The managed baseline is the deployed upstream
  tag `v0.1.1-beta-0.17`; avoid unrelated upgrades during enforcement changes.
- Read README and the managed-plan documentation before changes.
- Quotas are per isolated instance across organizations; no customer admin bypass.
- Write meaningful boundary and concurrency tests against disposable PostgreSQL.
  Run the upstream regression suite before publishing a derived release.
