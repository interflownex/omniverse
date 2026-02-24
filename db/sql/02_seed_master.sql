INSERT OR IGNORE INTO tenant (id, slug, legal_name, default_region_code, timezone, currency_code, locale)
VALUES ('tenant-nexora-default', 'nexora-default', 'NEXORA Default Tenant', 'sa-east', 'America/Sao_Paulo', 'BRL', 'pt-BR');

INSERT OR IGNORE INTO tenant_residency_policy (id, tenant_id, residency_region_code, pii_storage_required, cross_region_replication)
VALUES ('res-1', 'tenant-nexora-default', 'sa-east', 1, 1);

INSERT OR IGNORE INTO tenant_routing_policy (id, tenant_id, read_region_code, write_region_code, failover_region_code, strategy)
VALUES ('route-1', 'tenant-nexora-default', 'sa-east', 'us-east', 'eu-west', 'active-primary-write');

INSERT OR IGNORE INTO deployment_target (id, profile, environment, region_code, is_enabled) VALUES
('dep-local', 'NANO-LOCAL-13MB', 'local-nano', 'sa-east', 1),
('dep-stage', 'GLOBAL-SCALE', 'staging-global', 'us-east', 1),
('dep-prod', 'GLOBAL-SCALE', 'prod-global', 'us-east', 1);

INSERT OR IGNORE INTO system_module (id, code, name, enabled, sla_minutes) VALUES
('mod-auth', 'auth', 'IAM/Auth', 1, 5),
('mod-users', 'users', 'Users and Profiles', 1, 15),
('mod-catalog', 'catalog', 'Service Catalog', 1, 20),
('mod-workflow', 'workflow', 'Service Workflow', 1, 30),
('mod-notify', 'notify', 'Notifications', 1, 5),
('mod-billing', 'billing', 'Billing', 1, 60),
('mod-analytics', 'analytics', 'Analytics', 1, 45),
('mod-config', 'config', 'Tenant Configuration', 1, 15);

INSERT OR IGNORE INTO module_dependency (id, module_code, depends_on_code) VALUES
('md-1', 'workflow', 'auth'),
('md-2', 'workflow', 'users'),
('md-3', 'billing', 'workflow'),
('md-4', 'analytics', 'workflow');

INSERT OR IGNORE INTO replication_health (id, source_region_code, target_region_code, lag_ms, status) VALUES
('rep-1', 'us-east', 'eu-west', 210, 'healthy'),
('rep-2', 'us-east', 'sa-east', 320, 'healthy'),
('rep-3', 'sa-east', 'us-east', 290, 'healthy');

INSERT OR IGNORE INTO party (id, tenant_id, party_type, display_name, status) VALUES
('party-pf-admin', 'tenant-nexora-default', 'PF', 'Administrador NEXORA', 'active'),
('party-pf-analyst', 'tenant-nexora-default', 'PF', 'Analista NEXORA', 'active'),
('party-pf-operator', 'tenant-nexora-default', 'PF', 'Operador NEXORA', 'active'),
('party-pj-client', 'tenant-nexora-default', 'PJ', 'Cliente Empresarial Alpha Ltda', 'active');

INSERT OR IGNORE INTO person_profile (party_id, full_name, social_name, birth_date, nationality, marital_status, cpf, rg)
VALUES
('party-pf-admin', 'Administrador NEXORA', NULL, '1990-01-10', 'BR', 'single', '11111111111', 'RG100'),
('party-pf-analyst', 'Analista NEXORA', NULL, '1992-02-14', 'BR', 'single', '22222222222', 'RG200'),
('party-pf-operator', 'Operador NEXORA', NULL, '1995-03-19', 'BR', 'single', '33333333333', 'RG300');

INSERT OR IGNORE INTO legal_entity_profile (party_id, legal_name, trade_name, cnpj, cnae, legal_nature)
VALUES ('party-pj-client', 'Cliente Empresarial Alpha Ltda', 'Alpha', '12345678000199', '6201501', 'LTDA');

INSERT OR IGNORE INTO party_contact (id, party_id, contact_type, value, is_primary) VALUES
('ct-1', 'party-pf-admin', 'email', 'admin@nexora.local', 1),
('ct-2', 'party-pf-analyst', 'email', 'analyst@nexora.local', 1),
('ct-3', 'party-pf-operator', 'email', 'operator@nexora.local', 1),
('ct-4', 'party-pj-client', 'email', 'contato@alpha.local', 1);

INSERT OR IGNORE INTO party_address (id, party_id, address_type, line1, district, city, state, postal_code, country_code, is_primary) VALUES
('addr-1', 'party-pj-client', 'headquarter', 'Avenida Central 1000', 'Centro', 'Sao Paulo', 'SP', '01000000', 'BR', 1);

INSERT OR IGNORE INTO corporate_shareholding (id, company_party_id, shareholder_party_id, percentage)
VALUES ('share-1', 'party-pj-client', 'party-pf-admin', 60.0);

INSERT OR IGNORE INTO legal_representative (id, represented_party_id, representative_party_id, role)
VALUES ('repr-1', 'party-pj-client', 'party-pf-admin', 'CEO');

INSERT OR IGNORE INTO service_catalog (id, code, name, description, enabled) VALUES
('svc-1', 'onboarding', 'Onboarding Empresarial', 'Abertura e configuração de conta corporativa', 1),
('svc-2', 'incident-response', 'Resposta a Incidente', 'Gestão de incidente com SLA crítico', 1),
('svc-3', 'billing-review', 'Revisão de Faturamento', 'Revisão de cobrança e impostos', 1);

INSERT OR IGNORE INTO service_catalog_version (id, service_catalog_id, version, spec_json, is_active)
VALUES
('sv-1', 'svc-1', '24.8.0', '{"steps":["collect_docs","verify_identity","activate"]}', 1),
('sv-2', 'svc-2', '24.8.0', '{"steps":["triage","escalate","resolve"]}', 1),
('sv-3', 'svc-3', '24.8.0', '{"steps":["fetch_invoice","analyze","close"]}', 1);

INSERT OR IGNORE INTO workflow_definition (id, module_code, name, version)
VALUES ('wf-1', 'workflow', 'Default Request Workflow', '24.8.0');

INSERT OR IGNORE INTO workflow_transition (id, workflow_definition_id, from_state, action, to_state) VALUES
('wft-1', 'wf-1', 'open', 'start', 'in_progress'),
('wft-2', 'wf-1', 'in_progress', 'resolve', 'resolved'),
('wft-3', 'wf-1', 'resolved', 'close', 'closed'),
('wft-4', 'wf-1', 'in_progress', 'escalate', 'escalated');

INSERT OR IGNORE INTO sla_policy (id, module_code, priority, target_minutes) VALUES
('sla-1', 'workflow', 'high', 30),
('sla-2', 'workflow', 'medium', 120),
('sla-3', 'workflow', 'low', 480);

INSERT OR IGNORE INTO billing_account (id, tenant_id, party_id, status)
VALUES ('ba-1', 'tenant-nexora-default', 'party-pj-client', 'active');

INSERT OR IGNORE INTO invoice (id, billing_account_id, issue_date, due_date, status, currency_code, total_amount)
VALUES ('inv-1', 'ba-1', date('now','-2 day'), date('now','+15 day'), 'open', 'BRL', 3250.50);

INSERT OR IGNORE INTO invoice_line (id, invoice_id, item_code, description, quantity, unit_amount, line_total)
VALUES ('invl-1', 'inv-1', 'SVC-INC', 'Resposta a incidente crítico', 1, 3000.00, 3000.00),
       ('invl-2', 'inv-1', 'SVC-TAX', 'Taxa operacional', 1, 250.50, 250.50);

INSERT OR IGNORE INTO tax_item (id, invoice_line_id, tax_type, rate, amount)
VALUES ('tax-1', 'invl-1', 'ISS', 0.05, 150.00);

INSERT OR IGNORE INTO auth_user (id, tenant_id, party_id, name, email, password_hash, role_code, status)
VALUES
('usr-admin', 'tenant-nexora-default', 'party-pf-admin', 'Admin', 'admin@nexora.local', 'admin247', 'admin', 'active'),
('usr-user', 'tenant-nexora-default', 'party-pf-operator', 'User', 'user@nexora.local', 'user247', 'user', 'active'),
('usr-analyst', 'tenant-nexora-default', 'party-pf-analyst', 'Analyst', 'analyst@nexora.local', 'analyst247', 'analyst', 'active'),
('usr-operator', 'tenant-nexora-default', 'party-pf-operator', 'Operator', 'operator@nexora.local', 'operator247', 'operator', 'active');

INSERT OR IGNORE INTO service_request (id, tenant_id, module_code, title, description, status, priority, requester_party_id, assignee_user_id)
VALUES
('req-1', 'tenant-nexora-default', 'workflow', 'Incidente de API global', 'Timeout intermitente em us-east', 'in_progress', 'high', 'party-pj-client', 'usr-analyst'),
('req-2', 'tenant-nexora-default', 'billing', 'Revisão de fatura de janeiro', 'Diferença em imposto retido', 'open', 'medium', 'party-pj-client', 'usr-operator');

INSERT OR IGNORE INTO service_request_party (id, service_request_id, party_id, role)
VALUES
('srp-1', 'req-1', 'party-pj-client', 'requester'),
('srp-2', 'req-2', 'party-pj-client', 'requester');

INSERT OR IGNORE INTO workflow_execution_history (id, service_request_id, from_state, action, to_state, acted_by_user_id)
VALUES
('wh-1', 'req-1', 'open', 'start', 'in_progress', 'usr-analyst');

INSERT OR IGNORE INTO notification_event (id, tenant_id, user_id, channel, severity, title, body)
VALUES
('not-1', 'tenant-nexora-default', 'usr-analyst', 'in_app', 'critical', 'Incidente crítico', 'Serviço us-east com degradação detectada.'),
('not-2', 'tenant-nexora-default', 'usr-operator', 'in_app', 'high', 'Fatura pendente', 'Fatura inv-1 com vencimento próximo.');

INSERT OR IGNORE INTO consent_term (id, code, version, content)
VALUES ('cterm-1', 'privacy-policy', '24.8.0', 'Termo de privacidade NEXORA v24.8.0');

INSERT OR IGNORE INTO consent_grant (id, party_id, consent_term_id)
VALUES ('cgrant-1', 'party-pf-admin', 'cterm-1');

INSERT OR IGNORE INTO external_system (id, code, name, base_url)
VALUES ('ext-1', 'billing-sandbox', 'Billing Sandbox', 'https://sandbox.billing.local');

INSERT OR IGNORE INTO external_endpoint (id, external_system_id, endpoint_code, method, path)
VALUES ('exte-1', 'ext-1', 'invoice-sync', 'POST', '/v1/invoices/sync');

INSERT OR IGNORE INTO integration_credential_ref (id, external_system_id, secret_ref, rotation_days)
VALUES ('cred-1', 'ext-1', 'vault://nexora/billing-sandbox', 90);

INSERT OR IGNORE INTO integration_link (id, external_system_id, local_entity_type, local_entity_id, external_ref, status)
VALUES ('il-1', 'ext-1', 'invoice', 'inv-1', 'sandbox-inv-9001', 'active');

INSERT OR IGNORE INTO entity_attribute_def (id, entity_type, code, data_type, is_required, is_unique)
VALUES
('ead-1', 'party', 'customer_tier', 'text', 0, 0),
('ead-2', 'service_request', 'impact_score', 'number', 0, 0);

INSERT OR IGNORE INTO entity_attribute_value (id, entity_type, entity_id, attribute_code, value_text, value_number)
VALUES
('eav-1', 'party', 'party-pj-client', 'customer_tier', 'gold', NULL),
('eav-2', 'service_request', 'req-1', 'impact_score', NULL, 9.5);

INSERT OR IGNORE INTO entity_link (id, from_entity_type, from_entity_id, to_entity_type, to_entity_id, relation_code, metadata_json)
VALUES ('el-1', 'service_request', 'req-1', 'invoice', 'inv-1', 'related_billing', '{"reason":"incident_cost"}');

INSERT OR IGNORE INTO kpi_daily (id, tenant_id, day, open_requests, overdue_requests, revenue, notifications_unread)
VALUES ('kpi-1', 'tenant-nexora-default', date('now'), 1, 0, 3250.50, 2);

INSERT OR IGNORE INTO metric_snapshot (id, metric_code, tenant_id, region_code, value)
VALUES
('ms-1', 'open_requests', 'tenant-nexora-default', 'sa-east', 1),
('ms-2', 'overdue_requests', 'tenant-nexora-default', 'sa-east', 0),
('ms-3', 'daily_revenue', 'tenant-nexora-default', 'sa-east', 3250.50);
