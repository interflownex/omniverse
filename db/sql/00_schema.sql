PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS schema_release (
  id TEXT PRIMARY KEY,
  version TEXT NOT NULL,
  checksum TEXT NOT NULL,
  applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS geo_region (
  code TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  is_active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS tenant (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  legal_name TEXT NOT NULL,
  default_region_code TEXT NOT NULL,
  timezone TEXT NOT NULL DEFAULT 'UTC',
  currency_code TEXT NOT NULL DEFAULT 'BRL',
  locale TEXT NOT NULL DEFAULT 'pt-BR',
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(default_region_code) REFERENCES geo_region(code)
);

CREATE TABLE IF NOT EXISTS tenant_residency_policy (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  residency_region_code TEXT NOT NULL,
  pii_storage_required INTEGER NOT NULL DEFAULT 1,
  cross_region_replication INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(tenant_id) REFERENCES tenant(id),
  FOREIGN KEY(residency_region_code) REFERENCES geo_region(code)
);

CREATE TABLE IF NOT EXISTS tenant_routing_policy (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  read_region_code TEXT NOT NULL,
  write_region_code TEXT NOT NULL,
  failover_region_code TEXT,
  strategy TEXT NOT NULL DEFAULT 'active-primary-write',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(tenant_id) REFERENCES tenant(id),
  FOREIGN KEY(read_region_code) REFERENCES geo_region(code),
  FOREIGN KEY(write_region_code) REFERENCES geo_region(code),
  FOREIGN KEY(failover_region_code) REFERENCES geo_region(code)
);

CREATE TABLE IF NOT EXISTS deployment_target (
  id TEXT PRIMARY KEY,
  profile TEXT NOT NULL,
  environment TEXT NOT NULL,
  region_code TEXT,
  is_enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(region_code) REFERENCES geo_region(code)
);

CREATE TABLE IF NOT EXISTS system_module (
  id TEXT PRIMARY KEY,
  code TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  sla_minutes INTEGER NOT NULL DEFAULT 30,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS module_dependency (
  id TEXT PRIMARY KEY,
  module_code TEXT NOT NULL,
  depends_on_code TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(module_code) REFERENCES system_module(code),
  FOREIGN KEY(depends_on_code) REFERENCES system_module(code)
);

CREATE TABLE IF NOT EXISTS replication_health (
  id TEXT PRIMARY KEY,
  source_region_code TEXT NOT NULL,
  target_region_code TEXT NOT NULL,
  lag_ms INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'healthy',
  checked_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(source_region_code) REFERENCES geo_region(code),
  FOREIGN KEY(target_region_code) REFERENCES geo_region(code)
);

CREATE TABLE IF NOT EXISTS id_registry (
  id TEXT PRIMARY KEY,
  entity_type TEXT NOT NULL,
  region_code TEXT NOT NULL,
  tenant_id TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(region_code) REFERENCES geo_region(code),
  FOREIGN KEY(tenant_id) REFERENCES tenant(id)
);

CREATE TABLE IF NOT EXISTS party (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  party_type TEXT NOT NULL CHECK (party_type IN ('PF', 'PJ')),
  display_name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(tenant_id) REFERENCES tenant(id)
);

CREATE TABLE IF NOT EXISTS person_profile (
  party_id TEXT PRIMARY KEY,
  full_name TEXT NOT NULL,
  social_name TEXT,
  birth_date TEXT,
  nationality TEXT,
  marital_status TEXT,
  cpf TEXT UNIQUE,
  rg TEXT,
  passport TEXT,
  mother_name TEXT,
  father_name TEXT,
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS legal_entity_profile (
  party_id TEXT PRIMARY KEY,
  legal_name TEXT NOT NULL,
  trade_name TEXT,
  cnpj TEXT UNIQUE,
  state_registration TEXT,
  municipal_registration TEXT,
  cnae TEXT,
  legal_nature TEXT,
  foundation_date TEXT,
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS party_status_history (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  old_status TEXT,
  new_status TEXT NOT NULL,
  changed_by_user_id TEXT,
  changed_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS party_tags (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  tag TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS party_document (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  doc_type TEXT NOT NULL,
  doc_number TEXT NOT NULL,
  issuer TEXT,
  issued_at TEXT,
  expires_at TEXT,
  country_code TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(party_id, doc_type, doc_number),
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS party_tax_profile (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  tax_regime TEXT,
  withholding_profile TEXT,
  is_tax_exempt INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS party_verification (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  verification_type TEXT NOT NULL,
  status TEXT NOT NULL,
  verified_at TEXT,
  evidence_ref TEXT,
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS kyc_check (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  provider TEXT,
  result TEXT NOT NULL,
  score INTEGER,
  checked_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS aml_screening (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  provider TEXT,
  result TEXT NOT NULL,
  risk_level TEXT,
  checked_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS party_contact (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  contact_type TEXT NOT NULL,
  value TEXT NOT NULL,
  is_primary INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS party_address (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  address_type TEXT NOT NULL,
  line1 TEXT NOT NULL,
  line2 TEXT,
  district TEXT,
  city TEXT,
  state TEXT,
  postal_code TEXT,
  country_code TEXT NOT NULL DEFAULT 'BR',
  is_primary INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS party_address_history (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  old_address_id TEXT,
  new_address_id TEXT,
  changed_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS party_digital_channel (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  channel TEXT NOT NULL,
  handle TEXT NOT NULL,
  is_verified INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS corporate_shareholding (
  id TEXT PRIMARY KEY,
  company_party_id TEXT NOT NULL,
  shareholder_party_id TEXT NOT NULL,
  percentage REAL NOT NULL,
  effective_from TEXT,
  effective_to TEXT,
  FOREIGN KEY(company_party_id) REFERENCES party(id),
  FOREIGN KEY(shareholder_party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS legal_representative (
  id TEXT PRIMARY KEY,
  represented_party_id TEXT NOT NULL,
  representative_party_id TEXT NOT NULL,
  role TEXT,
  valid_from TEXT,
  valid_to TEXT,
  FOREIGN KEY(represented_party_id) REFERENCES party(id),
  FOREIGN KEY(representative_party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS power_of_attorney (
  id TEXT PRIMARY KEY,
  represented_party_id TEXT NOT NULL,
  proxy_party_id TEXT NOT NULL,
  scope TEXT,
  valid_from TEXT,
  valid_to TEXT,
  document_ref TEXT,
  FOREIGN KEY(represented_party_id) REFERENCES party(id),
  FOREIGN KEY(proxy_party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS party_relationship (
  id TEXT PRIMARY KEY,
  from_party_id TEXT NOT NULL,
  to_party_id TEXT NOT NULL,
  relationship_type TEXT NOT NULL,
  metadata_json TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(from_party_id) REFERENCES party(id),
  FOREIGN KEY(to_party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS service_catalog (
  id TEXT PRIMARY KEY,
  code TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS service_catalog_version (
  id TEXT PRIMARY KEY,
  service_catalog_id TEXT NOT NULL,
  version TEXT NOT NULL,
  spec_json TEXT,
  is_active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(service_catalog_id) REFERENCES service_catalog(id)
);

CREATE TABLE IF NOT EXISTS workflow_definition (
  id TEXT PRIMARY KEY,
  module_code TEXT NOT NULL,
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(module_code) REFERENCES system_module(code)
);

CREATE TABLE IF NOT EXISTS workflow_transition (
  id TEXT PRIMARY KEY,
  workflow_definition_id TEXT NOT NULL,
  from_state TEXT NOT NULL,
  action TEXT NOT NULL,
  to_state TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(workflow_definition_id) REFERENCES workflow_definition(id)
);

CREATE TABLE IF NOT EXISTS sla_policy (
  id TEXT PRIMARY KEY,
  module_code TEXT NOT NULL,
  priority TEXT NOT NULL,
  target_minutes INTEGER NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(module_code) REFERENCES system_module(code)
);

CREATE TABLE IF NOT EXISTS service_request (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  module_code TEXT NOT NULL,
  title TEXT NOT NULL,
  description TEXT,
  status TEXT NOT NULL,
  priority TEXT NOT NULL,
  requester_party_id TEXT,
  assignee_user_id TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(tenant_id) REFERENCES tenant(id),
  FOREIGN KEY(module_code) REFERENCES system_module(code),
  FOREIGN KEY(requester_party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS service_request_party (
  id TEXT PRIMARY KEY,
  service_request_id TEXT NOT NULL,
  party_id TEXT NOT NULL,
  role TEXT NOT NULL,
  FOREIGN KEY(service_request_id) REFERENCES service_request(id),
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS workflow_execution_history (
  id TEXT PRIMARY KEY,
  service_request_id TEXT NOT NULL,
  from_state TEXT,
  action TEXT NOT NULL,
  to_state TEXT NOT NULL,
  acted_by_user_id TEXT,
  acted_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(service_request_id) REFERENCES service_request(id)
);

CREATE TABLE IF NOT EXISTS billing_account (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  party_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(tenant_id) REFERENCES tenant(id),
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS invoice (
  id TEXT PRIMARY KEY,
  billing_account_id TEXT NOT NULL,
  issue_date TEXT NOT NULL,
  due_date TEXT NOT NULL,
  status TEXT NOT NULL,
  currency_code TEXT NOT NULL DEFAULT 'BRL',
  total_amount REAL NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(billing_account_id) REFERENCES billing_account(id)
);

CREATE TABLE IF NOT EXISTS invoice_line (
  id TEXT PRIMARY KEY,
  invoice_id TEXT NOT NULL,
  item_code TEXT,
  description TEXT,
  quantity REAL NOT NULL,
  unit_amount REAL NOT NULL,
  line_total REAL NOT NULL,
  FOREIGN KEY(invoice_id) REFERENCES invoice(id)
);

CREATE TABLE IF NOT EXISTS tax_item (
  id TEXT PRIMARY KEY,
  invoice_line_id TEXT NOT NULL,
  tax_type TEXT NOT NULL,
  rate REAL NOT NULL,
  amount REAL NOT NULL,
  FOREIGN KEY(invoice_line_id) REFERENCES invoice_line(id)
);

CREATE TABLE IF NOT EXISTS payment (
  id TEXT PRIMARY KEY,
  invoice_id TEXT NOT NULL,
  paid_at TEXT,
  amount REAL NOT NULL,
  method TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(invoice_id) REFERENCES invoice(id)
);

CREATE TABLE IF NOT EXISTS payment_allocation (
  id TEXT PRIMARY KEY,
  payment_id TEXT NOT NULL,
  invoice_line_id TEXT,
  amount REAL NOT NULL,
  FOREIGN KEY(payment_id) REFERENCES payment(id),
  FOREIGN KEY(invoice_line_id) REFERENCES invoice_line(id)
);

CREATE TABLE IF NOT EXISTS role (
  id TEXT PRIMARY KEY,
  code TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS permission (
  id TEXT PRIMARY KEY,
  code TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS role_permission (
  id TEXT PRIMARY KEY,
  role_code TEXT NOT NULL,
  permission_code TEXT NOT NULL,
  FOREIGN KEY(role_code) REFERENCES role(code),
  FOREIGN KEY(permission_code) REFERENCES permission(code)
);

CREATE TABLE IF NOT EXISTS auth_user (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  party_id TEXT,
  name TEXT NOT NULL,
  email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role_code TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  expires_at TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(tenant_id) REFERENCES tenant(id),
  FOREIGN KEY(party_id) REFERENCES party(id),
  FOREIGN KEY(role_code) REFERENCES role(code)
);

CREATE TABLE IF NOT EXISTS auth_session (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  access_token TEXT NOT NULL UNIQUE,
  refresh_token TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  expires_at TEXT NOT NULL,
  refresh_expires_at TEXT NOT NULL,
  is_revoked INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY(user_id) REFERENCES auth_user(id)
);

CREATE TABLE IF NOT EXISTS notification_event (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  channel TEXT NOT NULL,
  severity TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  read_at TEXT,
  FOREIGN KEY(tenant_id) REFERENCES tenant(id),
  FOREIGN KEY(user_id) REFERENCES auth_user(id)
);

CREATE TABLE IF NOT EXISTS audit_event (
  id TEXT PRIMARY KEY,
  tenant_id TEXT,
  actor_user_id TEXT,
  action TEXT NOT NULL,
  entity_type TEXT,
  entity_id TEXT,
  metadata_json TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(tenant_id) REFERENCES tenant(id),
  FOREIGN KEY(actor_user_id) REFERENCES auth_user(id)
);

CREATE TABLE IF NOT EXISTS data_change_log (
  id TEXT PRIMARY KEY,
  entity_type TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  change_type TEXT NOT NULL,
  before_json TEXT,
  after_json TEXT,
  changed_by_user_id TEXT,
  changed_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(changed_by_user_id) REFERENCES auth_user(id)
);

CREATE TABLE IF NOT EXISTS consent_term (
  id TEXT PRIMARY KEY,
  code TEXT NOT NULL,
  version TEXT NOT NULL,
  content TEXT NOT NULL,
  published_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS consent_grant (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  consent_term_id TEXT NOT NULL,
  granted_at TEXT NOT NULL DEFAULT (datetime('now')),
  revoked_at TEXT,
  FOREIGN KEY(party_id) REFERENCES party(id),
  FOREIGN KEY(consent_term_id) REFERENCES consent_term(id)
);

CREATE TABLE IF NOT EXISTS lgpd_request (
  id TEXT PRIMARY KEY,
  party_id TEXT NOT NULL,
  request_type TEXT NOT NULL,
  status TEXT NOT NULL,
  requested_at TEXT NOT NULL DEFAULT (datetime('now')),
  closed_at TEXT,
  FOREIGN KEY(party_id) REFERENCES party(id)
);

CREATE TABLE IF NOT EXISTS external_system (
  id TEXT PRIMARY KEY,
  code TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  base_url TEXT,
  is_enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS external_endpoint (
  id TEXT PRIMARY KEY,
  external_system_id TEXT NOT NULL,
  endpoint_code TEXT NOT NULL,
  method TEXT NOT NULL,
  path TEXT NOT NULL,
  is_active INTEGER NOT NULL DEFAULT 1,
  FOREIGN KEY(external_system_id) REFERENCES external_system(id)
);

CREATE TABLE IF NOT EXISTS integration_credential_ref (
  id TEXT PRIMARY KEY,
  external_system_id TEXT NOT NULL,
  secret_ref TEXT NOT NULL,
  rotation_days INTEGER NOT NULL DEFAULT 90,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(external_system_id) REFERENCES external_system(id)
);

CREATE TABLE IF NOT EXISTS integration_link (
  id TEXT PRIMARY KEY,
  external_system_id TEXT NOT NULL,
  local_entity_type TEXT NOT NULL,
  local_entity_id TEXT NOT NULL,
  external_ref TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(external_system_id) REFERENCES external_system(id)
);

CREATE TABLE IF NOT EXISTS webhook_subscription (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  target_url TEXT NOT NULL,
  secret_ref TEXT,
  is_active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(tenant_id) REFERENCES tenant(id)
);

CREATE TABLE IF NOT EXISTS webhook_delivery_log (
  id TEXT PRIMARY KEY,
  webhook_subscription_id TEXT NOT NULL,
  payload_json TEXT,
  status_code INTEGER,
  attempted_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(webhook_subscription_id) REFERENCES webhook_subscription(id)
);

CREATE TABLE IF NOT EXISTS entity_attribute_def (
  id TEXT PRIMARY KEY,
  entity_type TEXT NOT NULL,
  code TEXT NOT NULL,
  data_type TEXT NOT NULL,
  is_required INTEGER NOT NULL DEFAULT 0,
  is_unique INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(entity_type, code)
);

CREATE TABLE IF NOT EXISTS entity_attribute_value (
  id TEXT PRIMARY KEY,
  entity_type TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  attribute_code TEXT NOT NULL,
  value_text TEXT,
  value_number REAL,
  value_date TEXT,
  value_bool INTEGER,
  value_json TEXT,
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS entity_link (
  id TEXT PRIMARY KEY,
  from_entity_type TEXT NOT NULL,
  from_entity_id TEXT NOT NULL,
  to_entity_type TEXT NOT NULL,
  to_entity_id TEXT NOT NULL,
  relation_code TEXT NOT NULL,
  metadata_json TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS entity_attachment (
  id TEXT PRIMARY KEY,
  entity_type TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  file_name TEXT NOT NULL,
  mime_type TEXT,
  storage_url TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS entity_note (
  id TEXT PRIMARY KEY,
  entity_type TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  body TEXT NOT NULL,
  created_by_user_id TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(created_by_user_id) REFERENCES auth_user(id)
);

CREATE TABLE IF NOT EXISTS event_fact (
  id TEXT PRIMARY KEY,
  tenant_id TEXT,
  event_type TEXT NOT NULL,
  entity_type TEXT,
  entity_id TEXT,
  payload_json TEXT,
  occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(tenant_id) REFERENCES tenant(id)
);

CREATE TABLE IF NOT EXISTS metric_definition (
  id TEXT PRIMARY KEY,
  code TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT,
  aggregation_rule TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS metric_snapshot (
  id TEXT PRIMARY KEY,
  metric_code TEXT NOT NULL,
  tenant_id TEXT,
  region_code TEXT,
  value REAL NOT NULL,
  captured_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(metric_code) REFERENCES metric_definition(code),
  FOREIGN KEY(tenant_id) REFERENCES tenant(id),
  FOREIGN KEY(region_code) REFERENCES geo_region(code)
);

CREATE TABLE IF NOT EXISTS kpi_daily (
  id TEXT PRIMARY KEY,
  tenant_id TEXT,
  day TEXT NOT NULL,
  open_requests INTEGER NOT NULL DEFAULT 0,
  overdue_requests INTEGER NOT NULL DEFAULT 0,
  revenue REAL NOT NULL DEFAULT 0,
  notifications_unread INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY(tenant_id) REFERENCES tenant(id)
);

CREATE TABLE IF NOT EXISTS outbox_event (
  id TEXT PRIMARY KEY,
  tenant_id TEXT,
  topic TEXT NOT NULL,
  partition_key TEXT,
  payload_json TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  processed_at TEXT,
  FOREIGN KEY(tenant_id) REFERENCES tenant(id)
);

CREATE TABLE IF NOT EXISTS inbox_event (
  id TEXT PRIMARY KEY,
  tenant_id TEXT,
  topic TEXT NOT NULL,
  source_region_code TEXT,
  dedupe_key TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  received_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(topic, dedupe_key),
  FOREIGN KEY(tenant_id) REFERENCES tenant(id),
  FOREIGN KEY(source_region_code) REFERENCES geo_region(code)
);
