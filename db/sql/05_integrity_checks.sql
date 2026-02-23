-- Each query should return 0 in a healthy schema population.

SELECT 'orphan_person_profile' AS check_name, COUNT(1) AS violations
FROM person_profile pp LEFT JOIN party p ON p.id = pp.party_id WHERE p.id IS NULL;

SELECT 'orphan_legal_entity_profile' AS check_name, COUNT(1) AS violations
FROM legal_entity_profile lp LEFT JOIN party p ON p.id = lp.party_id WHERE p.id IS NULL;

SELECT 'orphan_auth_user_tenant' AS check_name, COUNT(1) AS violations
FROM auth_user au LEFT JOIN tenant t ON t.id = au.tenant_id WHERE t.id IS NULL;

SELECT 'orphan_service_request_tenant' AS check_name, COUNT(1) AS violations
FROM service_request sr LEFT JOIN tenant t ON t.id = sr.tenant_id WHERE t.id IS NULL;

SELECT 'orphan_service_request_party' AS check_name, COUNT(1) AS violations
FROM service_request sr LEFT JOIN party p ON p.id = sr.requester_party_id WHERE sr.requester_party_id IS NOT NULL AND p.id IS NULL;

SELECT 'orphan_notifications_user' AS check_name, COUNT(1) AS violations
FROM notification_event ne LEFT JOIN auth_user au ON au.id = ne.user_id WHERE au.id IS NULL;

SELECT 'duplicate_entity_attribute_def' AS check_name, COUNT(1) AS violations
FROM (
  SELECT entity_type, code, COUNT(*) c
  FROM entity_attribute_def
  GROUP BY entity_type, code
  HAVING COUNT(*) > 1
);

SELECT 'invalid_routing_region' AS check_name, COUNT(1) AS violations
FROM tenant_routing_policy trp
LEFT JOIN geo_region gr1 ON gr1.code = trp.read_region_code
LEFT JOIN geo_region gr2 ON gr2.code = trp.write_region_code
WHERE gr1.code IS NULL OR gr2.code IS NULL;
