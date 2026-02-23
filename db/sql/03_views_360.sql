CREATE VIEW IF NOT EXISTS vw_party_360 AS
SELECT
  p.id,
  p.tenant_id,
  p.party_type,
  p.display_name,
  p.status,
  pf.cpf,
  pj.cnpj,
  c.value AS primary_email,
  a.city,
  a.state,
  p.created_at
FROM party p
LEFT JOIN person_profile pf ON pf.party_id = p.id
LEFT JOIN legal_entity_profile pj ON pj.party_id = p.id
LEFT JOIN party_contact c ON c.party_id = p.id AND c.contact_type = 'email' AND c.is_primary = 1
LEFT JOIN party_address a ON a.party_id = p.id AND a.is_primary = 1;

CREATE VIEW IF NOT EXISTS vw_request_overview AS
SELECT
  sr.id,
  sr.tenant_id,
  sr.module_code,
  sr.title,
  sr.status,
  sr.priority,
  sr.created_at,
  sr.updated_at,
  au.name AS assignee_name,
  p.display_name AS requester_name
FROM service_request sr
LEFT JOIN auth_user au ON au.id = sr.assignee_user_id
LEFT JOIN party p ON p.id = sr.requester_party_id;

CREATE VIEW IF NOT EXISTS vw_platform_routing AS
SELECT
  t.id AS tenant_id,
  t.slug,
  trp.read_region_code,
  trp.write_region_code,
  trp.failover_region_code,
  trp.strategy
FROM tenant t
JOIN tenant_routing_policy trp ON trp.tenant_id = t.id;

CREATE VIEW IF NOT EXISTS vw_replication_status AS
SELECT
  rh.id,
  rh.source_region_code,
  rh.target_region_code,
  rh.lag_ms,
  rh.status,
  rh.checked_at
FROM replication_health rh;

CREATE VIEW IF NOT EXISTS vw_analytics_overview AS
SELECT
  t.id AS tenant_id,
  t.slug,
  COALESCE(SUM(CASE WHEN sr.status IN ('open', 'in_progress', 'escalated') THEN 1 ELSE 0 END), 0) AS open_requests,
  COALESCE((SELECT COUNT(1) FROM notification_event ne JOIN auth_user au ON au.id = ne.user_id WHERE au.tenant_id = t.id AND ne.read_at IS NULL), 0) AS unread_notifications,
  COALESCE((SELECT SUM(i.total_amount) FROM billing_account ba JOIN invoice i ON i.billing_account_id = ba.id WHERE ba.tenant_id = t.id), 0) AS total_invoiced
FROM tenant t
LEFT JOIN service_request sr ON sr.tenant_id = t.id
GROUP BY t.id, t.slug;
