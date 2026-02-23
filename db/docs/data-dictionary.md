# Data Dictionary (NEXORA v24.7)

## Core principles
- Canonical entity root: `party` for both PF and PJ.
- Future-proof expansion via `entity_attribute_def/value`.
- Future-proof relationships via `entity_link`.
- Global routing and replication metadata in region-aware tables.

## Key entities
- `tenant`: tenant-level isolation and defaults.
- `party`: principal business entity (PF/PJ).
- `auth_user` + `auth_session`: IAM runtime.
- `service_request`: operational workflow object.
- `notification_event`: user alerts with ack lifecycle.
- `invoice` and related tables: test billing domain.
- `replication_health`: global-scale health surface.

## Global tables
- `geo_region`
- `tenant_residency_policy`
- `tenant_routing_policy`
- `replication_health`
- `outbox_event`
- `inbox_event`
- `id_registry`
- `schema_release`
