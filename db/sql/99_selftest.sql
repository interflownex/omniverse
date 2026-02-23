SELECT 'schema_release' AS metric, COUNT(*) AS value FROM schema_release
UNION ALL SELECT 'regions', COUNT(*) FROM geo_region
UNION ALL SELECT 'tenants', COUNT(*) FROM tenant
UNION ALL SELECT 'modules', COUNT(*) FROM system_module
UNION ALL SELECT 'parties', COUNT(*) FROM party
UNION ALL SELECT 'users', COUNT(*) FROM auth_user
UNION ALL SELECT 'service_requests', COUNT(*) FROM service_request
UNION ALL SELECT 'notifications', COUNT(*) FROM notification_event
UNION ALL SELECT 'invoices', COUNT(*) FROM invoice;
