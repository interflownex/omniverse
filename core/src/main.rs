use std::{env, net::SocketAddr, path::{Path, PathBuf}, process::Command, sync::Arc};

use anyhow::Context;
use axum::{
    extract::{Path as UrlPath, Request, State},
    http::{HeaderMap, HeaderValue, StatusCode},
    middleware::{self, Next},
    response::{Html, IntoResponse, Response},
    routing::{get, post},
    Json, Router,
};
use chrono::{Duration, Utc};
use rusqlite::{params, Connection, OptionalExtension};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use tower_http::{cors::CorsLayer, services::ServeDir, trace::TraceLayer};
use tracing::info;
use uuid::Uuid;

#[derive(Clone)]
struct AppState { config: Arc<AppConfig> }

#[derive(Clone)]
struct AppConfig {
    api_version: String,
    region: String,
    default_tenant: String,
    bind_addr: SocketAddr,
    root_dir: PathBuf,
    static_dir: PathBuf,
    core_db: PathBuf,
    audit_db: PathBuf,
    analytics_db: PathBuf,
}

#[derive(Debug)]
struct ApiError { status: StatusCode, message: String }

type ApiResult<T> = Result<T, ApiError>;

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        (self.status, Json(json!({"error": self.message, "status": self.status.as_u16()}))).into_response()
    }
}

impl From<rusqlite::Error> for ApiError {
    fn from(e: rusqlite::Error) -> Self { Self { status: StatusCode::INTERNAL_SERVER_ERROR, message: format!("database error: {e}") } }
}

#[derive(Serialize)]
struct AuthContext { user_id: String, tenant_id: String, name: String, email: String, role_code: String }

#[derive(Deserialize)]
struct LoginInput { email: String, password: String }

#[derive(Deserialize)]
struct RefreshInput { refresh_token: String }

#[derive(Deserialize)]
struct CreateRequestInput { module_code: String, title: String, description: Option<String>, priority: Option<String>, requester_party_id: Option<String>, assignee_user_id: Option<String> }

#[derive(Deserialize)]
struct RequestActionInput { action: String, assignee_user_id: Option<String> }

#[derive(Deserialize)]
struct FailoverInput { tenant_id: Option<String>, target_region: Option<String> }

#[derive(Deserialize)]
struct ProvisionTesterInput { email: Option<String>, expires_hours: Option<i64> }

#[derive(Deserialize)]
struct RevokeTesterInput { email: Option<String> }

#[derive(Deserialize)]
struct PersonaChatInput { message: String }

impl AppConfig {
    fn from_env() -> anyhow::Result<Self> {
        let root = env::var("NEXORA_ROOT").map(PathBuf::from).unwrap_or(env::current_dir()?);
        std::fs::create_dir_all(root.join("data"))?;
        let bind_addr = env::var("NEXORA_BIND").unwrap_or_else(|_| "127.0.0.1:8080".to_string()).parse::<SocketAddr>()?;
        Ok(Self {
            api_version: "24.7".to_string(),
            region: env::var("NEXORA_REGION").unwrap_or_else(|_| "sa-east".to_string()),
            default_tenant: env::var("NEXORA_TENANT").unwrap_or_else(|_| "tenant-nexora-default".to_string()),
            bind_addr,
            static_dir: root.join("core/static"),
            core_db: root.join("data/core.db"),
            audit_db: root.join("data/audit.db"),
            analytics_db: root.join("data/analytics.db"),
            root_dir: root,
        })
    }
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt().with_env_filter(tracing_subscriber::EnvFilter::from_default_env()).init();
    let config = AppConfig::from_env()?;
    ensure_static_files(&config).await?;
    init_databases(&config)?;

    let state = AppState { config: Arc::new(config) };

    let app = Router::new()
        .route("/", get(index))
        .route("/health", get(health))
        .route("/metrics", get(metrics))
        .route("/api/v24.7/openapi", get(openapi))
        .route("/api/v24.7/auth/login", post(login))
        .route("/api/v24.7/auth/refresh", post(refresh))
        .route("/api/v24.7/users/me", get(users_me))
        .route("/api/v24.7/modules", get(modules))
        .route("/api/v24.7/service-requests", post(create_service_request))
        .route("/api/v24.7/service-requests/:id", get(get_service_request))
        .route("/api/v24.7/service-requests/:id/actions", post(service_request_action))
        .route("/api/v24.7/notifications", get(list_notifications))
        .route("/api/v24.7/notifications/:id/ack", post(ack_notification))
        .route("/api/v24.7/analytics/overview", get(analytics_overview))
        .route("/api/v24.7/platform/regions", get(platform_regions))
        .route("/api/v24.7/platform/routing", get(platform_routing))
        .route("/api/v24.7/platform/replication-status", get(platform_replication_status))
        .route("/api/v24.7/platform/failover/simulate", post(platform_failover_simulate))
        .route("/api/v24.7/test-access/provision", post(provision_remote_tester))
        .route("/api/v24.7/test-access/revoke", post(revoke_remote_tester))
        .route("/api/v24.7/test-access/status", get(remote_test_status))
        .route("/api/v24.7/persona/status", get(persona_status))
        .route("/api/v24.7/persona/chat", post(persona_chat))
        .route("/api/v24.7/mobile/bootstrap", get(mobile_bootstrap))
        .route("/api/v24.7/admin/status", get(admin_status))
        .route("/api/v24.7/admin/publish-remote", post(admin_publish_remote))
        .route("/api/v24.7/admin/unpublish-remote", post(admin_unpublish_remote))
        .route("/api/v24.7/admin/create-tester", post(admin_create_tester))
        .route("/api/v24.7/admin/revoke-tester", post(admin_revoke_tester))
        .route("/api/v24.7/admin/healthcheck", post(admin_healthcheck))
        .route("/api/v24.7/admin/validate-internet", post(admin_validate_internet))
        .route("/infographic", get(infographic))
        .route("/investors", get(investors))
        .route("/downloads", get(downloads))
        .route("/admin", get(admin_panel))
        .nest_service("/static", ServeDir::new(state.config.static_dir.clone()))
        .layer(CorsLayer::permissive())
        .layer(TraceLayer::new_for_http())
        .layer(middleware::from_fn_with_state(state.clone(), response_headers))
        .with_state(state.clone());

    info!("nexora-core listening on {}", state.config.bind_addr);
    let listener = tokio::net::TcpListener::bind(state.config.bind_addr).await?;
    axum::serve(listener, app).await?;
    Ok(())
}

async fn response_headers(State(state): State<AppState>, req: Request, next: Next) -> Response {
    let tenant = req.headers().get("x-tenant").and_then(|v| v.to_str().ok()).unwrap_or(&state.config.default_tenant).to_string();
    let mut resp = next.run(req).await;
    resp.headers_mut().insert("x-api-version", HeaderValue::from_str(&state.config.api_version).unwrap_or(HeaderValue::from_static("24.7")));
    resp.headers_mut().insert("x-region", HeaderValue::from_str(&state.config.region).unwrap_or(HeaderValue::from_static("sa-east")));
    if let Ok(v) = HeaderValue::from_str(&tenant) { resp.headers_mut().insert("x-tenant", v); }
    resp
}

fn now_sql() -> String { Utc::now().format("%Y-%m-%d %H:%M:%S").to_string() }
fn in_hours_sql(h: i64) -> String { (Utc::now() + Duration::hours(h)).format("%Y-%m-%d %H:%M:%S").to_string() }
fn open_conn(state: &AppState) -> ApiResult<Connection> { Ok(Connection::open(&state.config.core_db)?) }

fn auth(headers: &HeaderMap, state: &AppState) -> ApiResult<AuthContext> {
    let auth_header = headers.get("authorization").and_then(|v| v.to_str().ok()).ok_or(ApiError{status: StatusCode::UNAUTHORIZED, message: "missing authorization".to_string()})?;
    let token = auth_header.strip_prefix("Bearer ").ok_or(ApiError{status: StatusCode::UNAUTHORIZED, message: "invalid bearer".to_string()})?;
    let conn = open_conn(state)?;
    let row = conn.query_row(
        "SELECT au.id, au.tenant_id, au.name, au.email, au.role_code FROM auth_session s JOIN auth_user au ON au.id=s.user_id WHERE s.access_token=?1 AND s.is_revoked=0 AND datetime(s.expires_at) > datetime('now') AND au.status='active' LIMIT 1",
        params![token],
        |r| Ok(AuthContext{ user_id: r.get(0)?, tenant_id: r.get(1)?, name: r.get(2)?, email: r.get(3)?, role_code: r.get(4)? })
    ).optional()?;
    row.ok_or(ApiError{status: StatusCode::UNAUTHORIZED, message: "token expired or invalid".to_string()})
}

fn read_sql(path: &Path) -> anyhow::Result<String> {
    std::fs::read_to_string(path).with_context(|| format!("unable to read {}", path.display()))
}

fn init_databases(config: &AppConfig) -> anyhow::Result<()> {
    let schema = read_sql(&config.root_dir.join("db/sql/00_schema.sql"))?;
    let refs = read_sql(&config.root_dir.join("db/sql/01_reference_codes.sql"))?;
    let seeds = read_sql(&config.root_dir.join("db/sql/02_seed_master.sql"))?;
    let views = read_sql(&config.root_dir.join("db/sql/03_views_360.sql"))?;
    let idx = read_sql(&config.root_dir.join("db/sql/04_indexes.sql"))?;
    let mem = read_sql(&config.root_dir.join("db/sql/06_memory_profile.sql"))?;

    let c = Connection::open(&config.core_db)?;
    c.execute_batch(&mem)?;
    c.execute_batch(&schema)?;
    c.execute_batch(&refs)?;
    c.execute_batch(&seeds)?;
    c.execute_batch(&views)?;
    c.execute_batch(&idx)?;

    let a = Connection::open(&config.audit_db)?;
    a.execute_batch(&mem)?;
    a.execute_batch("CREATE TABLE IF NOT EXISTS audit_event(id TEXT PRIMARY KEY, tenant_id TEXT, actor_user_id TEXT, action TEXT, entity_type TEXT, entity_id TEXT, metadata_json TEXT, created_at TEXT);")?;

    let an = Connection::open(&config.analytics_db)?;
    an.execute_batch(&mem)?;
    an.execute_batch("CREATE TABLE IF NOT EXISTS kpi_daily(id TEXT PRIMARY KEY, tenant_id TEXT, day TEXT, open_requests INTEGER, overdue_requests INTEGER, revenue REAL, notifications_unread INTEGER);")?;
    Ok(())
}

async fn ensure_static_files(config: &AppConfig) -> anyhow::Result<()> {
    tokio::fs::create_dir_all(&config.static_dir).await?;
    Ok(())
}
async fn index(State(state): State<AppState>) -> ApiResult<Html<String>> {
    let html = tokio::fs::read_to_string(state.config.static_dir.join("index.html"))
        .await
        .map_err(|e| ApiError { status: StatusCode::INTERNAL_SERVER_ERROR, message: format!("ui error: {e}") })?;
    Ok(Html(html))
}

async fn infographic(State(state): State<AppState>) -> ApiResult<Html<String>> {
    let html = tokio::fs::read_to_string(state.config.static_dir.join("infographic.html"))
        .await
        .map_err(|e| ApiError { status: StatusCode::INTERNAL_SERVER_ERROR, message: format!("infographic error: {e}") })?;
    Ok(Html(html))
}

async fn investors(State(state): State<AppState>) -> ApiResult<Html<String>> {
    let html = tokio::fs::read_to_string(state.config.static_dir.join("investors.html"))
        .await
        .map_err(|e| ApiError { status: StatusCode::INTERNAL_SERVER_ERROR, message: format!("investors error: {e}") })?;
    Ok(Html(html))
}

async fn downloads(State(state): State<AppState>) -> ApiResult<Html<String>> {
    let html = tokio::fs::read_to_string(state.config.static_dir.join("downloads.html"))
        .await
        .map_err(|e| ApiError { status: StatusCode::INTERNAL_SERVER_ERROR, message: format!("downloads error: {e}") })?;
    Ok(Html(html))
}

async fn admin_panel(State(state): State<AppState>) -> ApiResult<Html<String>> {
    let html = tokio::fs::read_to_string(state.config.static_dir.join("admin.html"))
        .await
        .map_err(|e| ApiError { status: StatusCode::INTERNAL_SERVER_ERROR, message: format!("admin ui error: {e}") })?;
    Ok(Html(html))
}

async fn health(State(state): State<AppState>) -> Json<Value> {
    Json(json!({
        "status": "healthy",
        "api_version": state.config.api_version,
        "region": state.config.region,
        "tenant": state.config.default_tenant,
        "utc": Utc::now().to_rfc3339(),
        "databases": {
            "core": state.config.core_db.exists(),
            "audit": state.config.audit_db.exists(),
            "analytics": state.config.analytics_db.exists()
        }
    }))
}

async fn metrics(State(state): State<AppState>) -> ApiResult<String> {
    let conn = open_conn(&state)?;
    let open_requests: i64 = conn.query_row("SELECT COUNT(1) FROM service_request WHERE status IN ('open','in_progress','escalated')", [], |r| r.get(0))?;
    let sessions: i64 = conn.query_row("SELECT COUNT(1) FROM auth_session WHERE is_revoked=0 AND datetime(expires_at) > datetime('now')", [], |r| r.get(0))?;
    Ok(format!("# TYPE nexora_open_requests gauge\nnexora_open_requests {}\n# TYPE nexora_active_sessions gauge\nnexora_active_sessions {}\n", open_requests, sessions))
}

async fn openapi() -> Json<Value> {
    Json(json!({
        "openapi": "3.1.0",
        "info": {"title": "NEXORA API", "version": "24.7"},
        "paths": {
            "/api/v24.7/auth/login": {"post": {}},
            "/api/v24.7/auth/refresh": {"post": {}},
            "/api/v24.7/users/me": {"get": {}},
            "/api/v24.7/modules": {"get": {}},
            "/api/v24.7/service-requests": {"post": {}},
            "/api/v24.7/notifications": {"get": {}},
            "/api/v24.7/analytics/overview": {"get": {}},
            "/api/v24.7/platform/regions": {"get": {}},
            "/api/v24.7/platform/routing": {"get": {}},
            "/api/v24.7/platform/replication-status": {"get": {}},
            "/api/v24.7/platform/failover/simulate": {"post": {}},
            "/api/v24.7/persona/status": {"get": {}},
            "/api/v24.7/persona/chat": {"post": {}},
            "/api/v24.7/mobile/bootstrap": {"get": {}},
            "/api/v24.7/persona/status": {"get": {}},
            "/api/v24.7/persona/chat": {"post": {}},
            "/api/v24.7/admin/status": {"get": {}},
            "/api/v24.7/admin/publish-remote": {"post": {}},
            "/api/v24.7/admin/unpublish-remote": {"post": {}},
            "/api/v24.7/admin/create-tester": {"post": {}},
            "/api/v24.7/admin/revoke-tester": {"post": {}},
            "/api/v24.7/admin/healthcheck": {"post": {}},
            "/api/v24.7/admin/validate-internet": {"post": {}}
        }
    }))
}

async fn login(State(state): State<AppState>, Json(input): Json<LoginInput>) -> ApiResult<Json<Value>> {
    let conn = open_conn(&state)?;
    let user = conn.query_row(
        "SELECT id, tenant_id, name, email, role_code, password_hash, status, expires_at FROM auth_user WHERE email=?1 LIMIT 1",
        params![input.email],
        |r| Ok((
            r.get::<_, String>(0)?, r.get::<_, String>(1)?, r.get::<_, String>(2)?, r.get::<_, String>(3)?,
            r.get::<_, String>(4)?, r.get::<_, String>(5)?, r.get::<_, String>(6)?, r.get::<_, Option<String>>(7)?
        ))
    ).optional()?;

    let (user_id, tenant_id, name, email, role_code, password_hash, status, expires_at) = user.ok_or(ApiError{status: StatusCode::UNAUTHORIZED, message: "invalid credentials".to_string()})?;
    if password_hash != input.password { return Err(ApiError{status: StatusCode::UNAUTHORIZED, message: "invalid credentials".to_string()}); }
    if status != "active" { return Err(ApiError{status: StatusCode::FORBIDDEN, message: "inactive user".to_string()}); }
    if let Some(exp) = expires_at {
        let expired: i64 = conn.query_row("SELECT CASE WHEN datetime(?1) <= datetime('now') THEN 1 ELSE 0 END", params![exp], |r| r.get(0))?;
        if expired == 1 { return Err(ApiError{status: StatusCode::FORBIDDEN, message: "user expired".to_string()}); }
    }

    let access_token = Uuid::new_v4().to_string();
    let refresh_token = Uuid::new_v4().to_string();
    let expires_at = in_hours_sql(8);
    let refresh_expires_at = in_hours_sql(24);
    conn.execute(
        "INSERT INTO auth_session (id,user_id,access_token,refresh_token,created_at,expires_at,refresh_expires_at,is_revoked) VALUES (?1,?2,?3,?4,?5,?6,?7,0)",
        params![Uuid::new_v4().to_string(), user_id, access_token, refresh_token, now_sql(), expires_at, refresh_expires_at]
    )?;

    Ok(Json(json!({
        "access_token": access_token,
        "refresh_token": refresh_token,
        "expires_at": expires_at,
        "user": {"user_id": user_id, "tenant_id": tenant_id, "name": name, "email": email, "role_code": role_code}
    })))
}

async fn refresh(State(state): State<AppState>, Json(input): Json<RefreshInput>) -> ApiResult<Json<Value>> {
    let conn = open_conn(&state)?;
    let user_id: String = conn.query_row(
        "SELECT user_id FROM auth_session WHERE refresh_token=?1 AND is_revoked=0 AND datetime(refresh_expires_at) > datetime('now') LIMIT 1",
        params![input.refresh_token],
        |r| r.get(0)
    ).optional()?.ok_or(ApiError{status: StatusCode::UNAUTHORIZED, message: "invalid refresh token".to_string()})?;

    conn.execute("UPDATE auth_session SET is_revoked=1 WHERE refresh_token=?1", params![input.refresh_token])?;
    let access_token = Uuid::new_v4().to_string();
    let refresh_token = Uuid::new_v4().to_string();
    conn.execute(
        "INSERT INTO auth_session (id,user_id,access_token,refresh_token,created_at,expires_at,refresh_expires_at,is_revoked) VALUES (?1,?2,?3,?4,?5,?6,?7,0)",
        params![Uuid::new_v4().to_string(), user_id, access_token, refresh_token, now_sql(), in_hours_sql(8), in_hours_sql(24)]
    )?;
    Ok(Json(json!({"access_token": access_token, "refresh_token": refresh_token, "expires_at": in_hours_sql(8)})))
}

async fn users_me(State(state): State<AppState>, headers: HeaderMap) -> ApiResult<Json<AuthContext>> {
    Ok(Json(auth(&headers, &state)?))
}

async fn modules(State(state): State<AppState>) -> ApiResult<Json<Value>> {
    let conn = open_conn(&state)?;
    let mut stmt = conn.prepare("SELECT code,name,enabled,sla_minutes FROM system_module ORDER BY code")?;
    let rows = stmt.query_map([], |r| Ok(json!({"code": r.get::<_, String>(0)?, "name": r.get::<_, String>(1)?, "enabled": r.get::<_, i64>(2)?==1, "sla_minutes": r.get::<_, i64>(3)?})))?;
    let mut list = Vec::new();
    for row in rows { list.push(row?); }
    Ok(Json(json!({"modules": list})))
}

async fn create_service_request(State(state): State<AppState>, headers: HeaderMap, Json(input): Json<CreateRequestInput>) -> ApiResult<Json<Value>> {
    let ctx = auth(&headers, &state)?;
    let conn = open_conn(&state)?;
    let id = format!("req-{}", Uuid::new_v4());
    let priority = input.priority.unwrap_or_else(|| "medium".to_string());
    let requester = input.requester_party_id.unwrap_or_else(|| "party-pj-client".to_string());
    let assignee = input.assignee_user_id.unwrap_or_else(|| ctx.user_id.clone());

    conn.execute(
        "INSERT INTO service_request (id,tenant_id,module_code,title,description,status,priority,requester_party_id,assignee_user_id,created_at,updated_at) VALUES (?1,?2,?3,?4,?5,'open',?6,?7,?8,?9,?10)",
        params![id, ctx.tenant_id, input.module_code, input.title, input.description, priority, requester, assignee, now_sql(), now_sql()]
    )?;

    Ok(Json(json!({"id": id, "status": "open", "priority": priority, "assignee_user_id": assignee})))
}
async fn get_service_request(State(state): State<AppState>, headers: HeaderMap, UrlPath(id): UrlPath<String>) -> ApiResult<Json<Value>> {
    let _ctx = auth(&headers, &state)?;
    let conn = open_conn(&state)?;
    let row: Option<Value> = conn.query_row(
        "SELECT id,module_code,title,description,status,priority,requester_party_id,assignee_user_id,created_at,updated_at FROM service_request WHERE id=?1 LIMIT 1",
        params![id],
        |r| Ok(json!({
            "id": r.get::<_, String>(0)?,
            "module_code": r.get::<_, String>(1)?,
            "title": r.get::<_, String>(2)?,
            "description": r.get::<_, Option<String>>(3)?,
            "status": r.get::<_, String>(4)?,
            "priority": r.get::<_, String>(5)?,
            "requester_party_id": r.get::<_, Option<String>>(6)?,
            "assignee_user_id": r.get::<_, Option<String>>(7)?,
            "created_at": r.get::<_, String>(8)?,
            "updated_at": r.get::<_, String>(9)?
        }))
    ).optional()?;
    Ok(Json(row.ok_or(ApiError{status: StatusCode::NOT_FOUND, message: "service request not found".to_string()})?))
}

async fn service_request_action(State(state): State<AppState>, headers: HeaderMap, UrlPath(id): UrlPath<String>, Json(input): Json<RequestActionInput>) -> ApiResult<Json<Value>> {
    let ctx = auth(&headers, &state)?;
    let conn = open_conn(&state)?;
    let current: String = conn.query_row("SELECT status FROM service_request WHERE id=?1", params![id], |r| r.get(0)).optional()?.ok_or(ApiError{status: StatusCode::NOT_FOUND, message: "service request not found".to_string()})?;
    let next = match input.action.to_lowercase().as_str() {
        "start" => "in_progress",
        "resolve" => "resolved",
        "close" => "closed",
        "escalate" => "escalated",
        "reopen" => "open",
        _ => return Err(ApiError{status: StatusCode::BAD_REQUEST, message: "invalid action".to_string()})
    };
    let assignee = input.assignee_user_id.unwrap_or(ctx.user_id.clone());
    conn.execute("UPDATE service_request SET status=?1, assignee_user_id=?2, updated_at=?3 WHERE id=?4", params![next, assignee, now_sql(), id])?;
    conn.execute("INSERT INTO workflow_execution_history (id,service_request_id,from_state,action,to_state,acted_by_user_id,acted_at) VALUES (?1,?2,?3,?4,?5,?6,?7)",
        params![Uuid::new_v4().to_string(), id, current, input.action, next, ctx.user_id, now_sql()])?;
    Ok(Json(json!({"id": id, "from": current, "to": next, "assignee_user_id": assignee, "updated_at": now_sql()})))
}

async fn list_notifications(State(state): State<AppState>, headers: HeaderMap) -> ApiResult<Json<Value>> {
    let ctx = auth(&headers, &state)?;
    let conn = open_conn(&state)?;
    let mut stmt = conn.prepare("SELECT id,channel,severity,title,body,created_at,read_at FROM notification_event WHERE user_id=?1 ORDER BY datetime(created_at) DESC")?;
    let rows = stmt.query_map(params![ctx.user_id], |r| Ok(json!({
        "id": r.get::<_, String>(0)?,
        "channel": r.get::<_, String>(1)?,
        "severity": r.get::<_, String>(2)?,
        "title": r.get::<_, String>(3)?,
        "body": r.get::<_, String>(4)?,
        "created_at": r.get::<_, String>(5)?,
        "read_at": r.get::<_, Option<String>>(6)?
    })))?;
    let mut list = Vec::new();
    for row in rows { list.push(row?); }
    Ok(Json(json!({"notifications": list})))
}

async fn ack_notification(State(state): State<AppState>, headers: HeaderMap, UrlPath(id): UrlPath<String>) -> ApiResult<Json<Value>> {
    let ctx = auth(&headers, &state)?;
    let conn = open_conn(&state)?;
    let changed = conn.execute("UPDATE notification_event SET read_at=?1 WHERE id=?2 AND user_id=?3", params![now_sql(), id, ctx.user_id])?;
    if changed == 0 { return Err(ApiError{status: StatusCode::NOT_FOUND, message: "notification not found".to_string()}); }
    Ok(Json(json!({"id": id, "acknowledged_at": now_sql()})))
}

async fn analytics_overview(State(state): State<AppState>, headers: HeaderMap) -> ApiResult<Json<Value>> {
    let ctx = auth(&headers, &state)?;
    let conn = open_conn(&state)?;
    let (open_requests, unread, invoiced): (i64, i64, f64) = conn.query_row(
        "SELECT open_requests, unread_notifications, total_invoiced FROM vw_analytics_overview WHERE tenant_id=?1 LIMIT 1",
        params![ctx.tenant_id],
        |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?))
    )?;
    Ok(Json(json!({"tenant_id": ctx.tenant_id, "open_requests": open_requests, "unread_notifications": unread, "total_invoiced": invoiced, "captured_at": now_sql()})))
}

async fn platform_regions(State(state): State<AppState>) -> ApiResult<Json<Value>> {
    let conn = open_conn(&state)?;
    let mut stmt = conn.prepare("SELECT code,name,is_active FROM geo_region ORDER BY code")?;
    let rows = stmt.query_map([], |r| Ok(json!({"code": r.get::<_, String>(0)?, "name": r.get::<_, String>(1)?, "is_active": r.get::<_, i64>(2)?==1})))?;
    let mut list = Vec::new();
    for row in rows { list.push(row?); }
    Ok(Json(json!({"regions": list})))
}

async fn platform_routing(State(state): State<AppState>) -> ApiResult<Json<Value>> {
    let conn = open_conn(&state)?;
    let mut stmt = conn.prepare("SELECT tenant_id,slug,read_region_code,write_region_code,failover_region_code,strategy FROM vw_platform_routing")?;
    let rows = stmt.query_map([], |r| Ok(json!({
        "tenant_id": r.get::<_, String>(0)?,
        "tenant_slug": r.get::<_, String>(1)?,
        "read_region": r.get::<_, String>(2)?,
        "write_region": r.get::<_, String>(3)?,
        "failover_region": r.get::<_, Option<String>>(4)?,
        "strategy": r.get::<_, String>(5)?
    })))?;
    let mut list = Vec::new();
    for row in rows { list.push(row?); }
    Ok(Json(json!({"routing": list})))
}

async fn platform_replication_status(State(state): State<AppState>) -> ApiResult<Json<Value>> {
    let conn = open_conn(&state)?;
    let mut stmt = conn.prepare("SELECT source_region_code,target_region_code,lag_ms,status,checked_at FROM vw_replication_status")?;
    let rows = stmt.query_map([], |r| Ok(json!({
        "source_region": r.get::<_, String>(0)?,
        "target_region": r.get::<_, String>(1)?,
        "lag_ms": r.get::<_, i64>(2)?,
        "status": r.get::<_, String>(3)?,
        "checked_at": r.get::<_, String>(4)?
    })))?;
    let mut list = Vec::new();
    for row in rows { list.push(row?); }
    Ok(Json(json!({"replication": list})))
}

async fn platform_failover_simulate(State(state): State<AppState>, headers: HeaderMap, Json(input): Json<FailoverInput>) -> ApiResult<Json<Value>> {
    let ctx = auth(&headers, &state)?;
    if ctx.role_code != "admin" { return Err(ApiError{status: StatusCode::FORBIDDEN, message: "admin required".to_string()}); }
    let tenant_id = input.tenant_id.unwrap_or(ctx.tenant_id.clone());
    let conn = open_conn(&state)?;
    let target = if let Some(t) = input.target_region { t } else {
        conn.query_row("SELECT COALESCE(failover_region_code,write_region_code) FROM tenant_routing_policy WHERE tenant_id=?1 LIMIT 1", params![tenant_id], |r| r.get(0))?
    };
    conn.execute("UPDATE tenant_routing_policy SET write_region_code=?1 WHERE tenant_id=?2", params![target, tenant_id])?;
    conn.execute("INSERT INTO audit_event (id,tenant_id,actor_user_id,action,entity_type,entity_id,metadata_json,created_at) VALUES (?1,?2,?3,'platform.failover.simulate','tenant_routing_policy',?4,?5,?6)",
        params![Uuid::new_v4().to_string(), tenant_id, ctx.user_id, tenant_id, json!({"target_region": target}).to_string(), now_sql()])?;
    Ok(Json(json!({"tenant_id": tenant_id, "new_write_region": target, "simulated_by": ctx.email, "simulated_at": now_sql()})))
}

async fn provision_remote_tester(State(state): State<AppState>, headers: HeaderMap, Json(input): Json<ProvisionTesterInput>) -> ApiResult<Json<Value>> {
    let ctx = auth(&headers, &state)?;
    if ctx.role_code != "admin" { return Err(ApiError{status: StatusCode::FORBIDDEN, message: "admin required".to_string()}); }
    let conn = open_conn(&state)?;
    let email = input.email.unwrap_or_else(|| "tester.remote@nexora.local".to_string()).to_lowercase();
    let pass = format!("T{}!", &Uuid::new_v4().simple().to_string()[..11]);
    let expires = in_hours_sql(input.expires_hours.unwrap_or(24));
    conn.execute("INSERT OR IGNORE INTO party (id,tenant_id,party_type,display_name,status) VALUES ('party-pf-tester',?1,'PF','Remote Tester','active')", params![ctx.tenant_id.clone()])?;
    conn.execute("INSERT OR IGNORE INTO person_profile (party_id,full_name,nationality,marital_status,cpf) VALUES ('party-pf-tester','Remote Tester','BR','single','99999999999')", [])?;
    conn.execute(
        "INSERT INTO auth_user (id,tenant_id,party_id,name,email,password_hash,role_code,status,expires_at) VALUES (?1,?2,'party-pf-tester','Remote Tester',?3,?4,'tester','active',?5) ON CONFLICT(email) DO UPDATE SET password_hash=excluded.password_hash,status='active',role_code='tester',expires_at=excluded.expires_at",
        params![format!("usr-tester-{}", Uuid::new_v4()), ctx.tenant_id, email, pass, expires]
    )?;
    Ok(Json(json!({"email": email, "password": pass, "expires_at": expires, "tenant_id": ctx.tenant_id})))
}

async fn revoke_remote_tester(State(state): State<AppState>, headers: HeaderMap, Json(input): Json<RevokeTesterInput>) -> ApiResult<Json<Value>> {
    let ctx = auth(&headers, &state)?;
    if ctx.role_code != "admin" { return Err(ApiError{status: StatusCode::FORBIDDEN, message: "admin required".to_string()}); }
    let email = input.email.unwrap_or_else(|| "tester.remote@nexora.local".to_string()).to_lowercase();
    let conn = open_conn(&state)?;
    let user: Option<String> = conn.query_row("SELECT id FROM auth_user WHERE email=?1 LIMIT 1", params![email], |r| r.get(0)).optional()?;
    if let Some(uid) = user {
        conn.execute("UPDATE auth_user SET status='inactive' WHERE id=?1", params![uid.clone()])?;
        conn.execute("UPDATE auth_session SET is_revoked=1 WHERE user_id=?1", params![uid])?;
    }
    Ok(Json(json!({"email": email, "revoked_at": now_sql()})))
}

async fn remote_test_status(State(state): State<AppState>, headers: HeaderMap) -> ApiResult<Json<Value>> {
    let _ctx = auth(&headers, &state)?;
    let conn = open_conn(&state)?;
    let tester: Option<(String, Option<String>)> = conn.query_row("SELECT status,expires_at FROM auth_user WHERE email='tester.remote@nexora.local' LIMIT 1", [], |r| Ok((r.get(0)?, r.get(1)?))).optional()?;
    let public_url = tokio::fs::read_to_string(state.config.root_dir.join("runtime/remote/public-url.txt")).await.ok().map(|v| v.trim().to_string());
    Ok(Json(json!({
        "public_url": public_url,
        "tester": tester.map(|(status, expires_at)| json!({"email": "tester.remote@nexora.local", "status": status, "expires_at": expires_at})),
        "checked_at": now_sql()
    })))
}

async fn persona_status() -> Json<Value> {
    Json(json!({
        "name": "Persona",
        "launch_ready": true,
        "mode": "operational",
        "description": "IA nativa do lançamento NEXORA para assistência operacional e cliente."
    }))
}

async fn persona_chat(State(state): State<AppState>, headers: HeaderMap, Json(input): Json<PersonaChatInput>) -> ApiResult<Json<Value>> {
    let ctx = auth(&headers, &state)?;
    let msg = input.message.trim();
    let answer = if msg.is_empty() {
        "Sou Persona. Envie uma pergunta sobre solicitações, módulos ou faturamento.".to_string()
    } else {
        format!("Sou Persona. Entendi sua solicitação: '{}'. Vou orientar o próximo passo no tenant {}.", msg, ctx.tenant_id)
    };
    Ok(Json(json!({
        "assistant": "Persona",
        "user": ctx.email,
        "tenant_id": ctx.tenant_id,
        "response": answer,
        "timestamp": now_sql()
    })))
}

async fn mobile_bootstrap(State(state): State<AppState>) -> Json<Value> {
    let preferred = env::var("NEXORA_PUBLIC_BASE_URL").unwrap_or_else(|_| "https://nexora.interflownex.com".to_string());
    let dynamic_public = std::fs::read_to_string(state.config.root_dir.join("runtime/remote/public-url.txt"))
        .ok()
        .map(|v| v.trim().to_string())
        .filter(|v| !v.is_empty());

    let mut fallbacks = vec![preferred.clone()];
    if let Some(url) = dynamic_public.clone() {
        if !fallbacks.contains(&url) { fallbacks.insert(0, url); }
    }

    Json(json!({
        "app": "NEXORA Mobile",
        "assistant": "Persona",
        "preferred_base_url": fallbacks.first().cloned().unwrap_or(preferred),
        "fallback_urls": fallbacks,
        "updated_at": now_sql()
    }))
}





fn require_admin(headers: &HeaderMap, state: &AppState) -> ApiResult<AuthContext> {
    let ctx = auth(headers, state)?;
    if ctx.role_code != "admin" {
        return Err(ApiError{status: StatusCode::FORBIDDEN, message: "admin required".to_string()});
    }
    Ok(ctx)
}

fn run_ps_script(state: &AppState, script_name: &str) -> ApiResult<Value> {
    let script_path = state.config.root_dir.join("scripts").join(script_name);
    if !script_path.exists() {
        return Err(ApiError{status: StatusCode::NOT_FOUND, message: format!("script not found: {}", script_name)});
    }

    let output = Command::new("powershell")
        .arg("-NoProfile")
        .arg("-ExecutionPolicy")
        .arg("Bypass")
        .arg("-File")
        .arg(script_path)
        .output()
        .map_err(|e| ApiError{status: StatusCode::INTERNAL_SERVER_ERROR, message: format!("script execution failed: {e}")})?;

    Ok(json!({
        "success": output.status.success(),
        "exit_code": output.status.code(),
        "stdout": String::from_utf8_lossy(&output.stdout).to_string(),
        "stderr": String::from_utf8_lossy(&output.stderr).to_string()
    }))
}

async fn admin_status(State(state): State<AppState>, headers: HeaderMap) -> ApiResult<Json<Value>> {
    let _ctx = require_admin(&headers, &state)?;
    let public_url = std::fs::read_to_string(state.config.root_dir.join("runtime/remote/public-url.txt")).ok();
    let tester = std::fs::read_to_string(state.config.root_dir.join("runtime/remote/tester-credentials.txt")).ok();
    Ok(Json(json!({
        "public_url": public_url.map(|s| s.trim().to_string()),
        "tester_credentials": tester,
        "checked_at": now_sql()
    })))
}

async fn admin_publish_remote(State(state): State<AppState>, headers: HeaderMap) -> ApiResult<Json<Value>> {
    let _ctx = require_admin(&headers, &state)?;
    Ok(Json(run_ps_script(&state, "publish-remote.ps1")?))
}

async fn admin_unpublish_remote(State(state): State<AppState>, headers: HeaderMap) -> ApiResult<Json<Value>> {
    let _ctx = require_admin(&headers, &state)?;
    Ok(Json(run_ps_script(&state, "unpublish-remote.ps1")?))
}

async fn admin_create_tester(State(state): State<AppState>, headers: HeaderMap) -> ApiResult<Json<Value>> {
    let _ctx = require_admin(&headers, &state)?;
    Ok(Json(run_ps_script(&state, "create-remote-tester.ps1")?))
}

async fn admin_revoke_tester(State(state): State<AppState>, headers: HeaderMap) -> ApiResult<Json<Value>> {
    let _ctx = require_admin(&headers, &state)?;
    Ok(Json(run_ps_script(&state, "revoke-remote-tester.ps1")?))
}

async fn admin_healthcheck(State(state): State<AppState>, headers: HeaderMap) -> ApiResult<Json<Value>> {
    let _ctx = require_admin(&headers, &state)?;
    Ok(Json(run_ps_script(&state, "healthcheck.ps1")?))
}

async fn admin_validate_internet(State(state): State<AppState>, headers: HeaderMap) -> ApiResult<Json<Value>> {
    let _ctx = require_admin(&headers, &state)?;
    Ok(Json(run_ps_script(&state, "validate-host-internet.ps1")?))
}
