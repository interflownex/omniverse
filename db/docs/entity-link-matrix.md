# Entity Link Matrix (NEXORA v24.8)

| From | To | Mechanism |
|---|---|---|
| PF/PJ (`party`) | Service Request (`service_request`) | `service_request.requester_party_id` + `service_request_party` |
| PF/PJ (`party`) | Invoice (`invoice`) | `billing_account.party_id -> invoice.billing_account_id` |
| Service Request | Invoice | `entity_link` relation `related_billing` |
| Tenant | Regions | `tenant_routing_policy`, `tenant_residency_policy` |
| External system | Local entities | `integration_link` |
| Any entity | Any entity | `entity_link` |
| Any entity | Dynamic attributes | `entity_attribute_value` |

## Integrity contract
- All critical links are either FK-backed or explicitly typed and indexed in `entity_link`.
- New relationship types do not require schema mutation.
