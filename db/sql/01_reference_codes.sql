INSERT OR IGNORE INTO schema_release (id, version, checksum) VALUES
('schema-24.7.0', '24.7.0', 'nexora-v24.7-baseline');

INSERT OR IGNORE INTO geo_region (code, name) VALUES
('us-east', 'United States East'),
('eu-west', 'Europe West'),
('sa-east', 'South America East');

INSERT OR IGNORE INTO role (id, code, name) VALUES
('role-admin', 'admin', 'Administrator'),
('role-analyst', 'analyst', 'Analyst'),
('role-operator', 'operator', 'Operator'),
('role-tester', 'tester', 'Remote Tester');

INSERT OR IGNORE INTO permission (id, code, name) VALUES
('perm-1', 'platform.failover.simulate', 'Simulate platform failover'),
('perm-2', 'service_request.create', 'Create service request'),
('perm-3', 'service_request.manage', 'Manage service request'),
('perm-4', 'notification.ack', 'Acknowledge notification');

INSERT OR IGNORE INTO role_permission (id, role_code, permission_code) VALUES
('rp-1', 'admin', 'platform.failover.simulate'),
('rp-2', 'admin', 'service_request.create'),
('rp-3', 'admin', 'service_request.manage'),
('rp-4', 'admin', 'notification.ack'),
('rp-5', 'analyst', 'service_request.create'),
('rp-6', 'analyst', 'service_request.manage'),
('rp-7', 'operator', 'service_request.create'),
('rp-8', 'operator', 'notification.ack'),
('rp-9', 'tester', 'service_request.create');

INSERT OR IGNORE INTO metric_definition (id, code, name, description, aggregation_rule) VALUES
('metric-1', 'open_requests', 'Open Requests', 'Current number of open service requests', 'count(status=open)'),
('metric-2', 'overdue_requests', 'Overdue Requests', 'Current number of overdue requests', 'count(sla_overdue=true)'),
('metric-3', 'daily_revenue', 'Daily Revenue', 'Daily billing revenue', 'sum(invoice.total_amount)');
