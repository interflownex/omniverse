# NEXORA v24.8

NEXORA v24.8 implements a dual-profile architecture:

- `NANO-LOCAL-13MB`: local test runtime using one Rust process + SQLite databases.
- `GLOBAL-SCALE`: Kubernetes multi-region topology with GitOps-ready manifests and observability stack.

## What is included

- Rust API runtime (`core`) exposing `/api/v24.8` endpoints.
- Canonical PF/PJ database model with extensibility (`entity_attribute_*`, `entity_link`).
- Local scripts for bootstrap, run, healthcheck, reset, uninstall, memory budget, and host internet validation.
- Remote test publication scripts through Cloudflare Tunnel.
- Global-scale infrastructure blueprints for k8s, HPA, network security, and observability.
- Design tokens and a responsive UI shell for web/mobile/smartwatch in the runtime static assets.

## Repo layout

- `core/`: Rust service (`nexora-core`)
- `db/`: SQL schema, references, seeds, checks, and docs
- `infra/`: global-scale k8s and observability manifests
- `packages/design-tokens`: NEXORA visual tokens
- `scripts/`: operational automation
- `apps/`: platform-specific guidance assets

## API baseline

All responses include:

- `x-api-version: 24.8`
- `x-region: <region_code>`
- `x-tenant: <tenant_id>`

Core endpoints include auth, users, modules, service requests, notifications, analytics, platform routing/regions/failover, and remote test access provisioning.

## Local quick start (PowerShell)

```powershell
.\scripts\bootstrap.ps1
.\scripts\up.ps1
.\scripts\healthcheck.ps1
```

For remote testing:

```powershell
.\scripts\remote-test-pack.ps1
```

For teardown:

```powershell
.\scripts\revoke-remote-tester.ps1
.\scripts\unpublish-remote.ps1
.\scripts\down.ps1
```

## Notes

- This environment currently has no Rust/Node preinstalled. `bootstrap.ps1` installs prerequisites when available via `winget`.
- The 13MB target is enforced for `nexora-core` process by `check-memory-budget.ps1`.

## Online infographic and investor portal

After starting the runtime:

- Infographic: `http://127.0.0.1:8080/infographic`
- Investor portal: `http://127.0.0.1:8080/investors`

## Manuals (PDF)

Generated manuals are available at:

- `docs/manuals/pdf/manual-servidor-local.pdf`
- `docs/manuals/pdf/manual-admin.pdf`
- `docs/manuals/pdf/manual-usuario.pdf`
