# Instruções para Agentes de IA - NEXORA v24.7

## Visão Geral do Projeto

NEXORA é uma plataforma multi-perfil escalável que opera em dois modos:
- **NANO-LOCAL-13MB**: Runtime local com um processo Rust + SQLite para testes
- **GLOBAL-SCALE**: Kubernetes multi-região com GitOps (us-east, eu-west, sa-east)

Implementa modelo canônico PF/PJ com extensibilidade via `entity_attribute_*` e `entity_link`.

## Arquitetura Principal

### Core (`core/`)
- **Framework**: Rust + Axum (servidor HTTP async)
- **API**: `/api/v24.7` com headers padrão (`x-api-version`, `x-region`, `x-tenant`)
- **Persistência**: 3 bancos SQLite (`core.db`, `audit.db`, `analytics.db`)
- **Estáticos**: Assets servidos de `core/static/` (HTML, JS, CSS responsivo)
- **Entidades principais**: tenant, party (PF/PJ), auth_user, auth_session, service_request, notification_event

### Database (`db/`)
- Schema versionado com SQL em `sql/` (schema > refs > seed > views > indexes > integridade)
- Tabelas globais para replicação (geo_region, tenant_residency_policy, tenant_routing_policy, replication_health, outbox_event)
- Documentação em `docs/data-dictionary.md` e `docs/entity-link-matrix.md`

### Infrastructure (`infra/`)
- Kustomize para k8s (base + overlays: prod-global, staging-global)
- Observabilidade: Prometheus + Grafana com dashboard `nexora-global-overview.json`
- Network policies de segurança em `security/network-policies.yaml`
- SLA: RTO ≤15min, RPO ≤5min; autoscale em CPU>60% ou latência p95 acima do target

### Apps (`apps/`)
- Interface canônica em `core/static/` (web, mobile PWA, smartwatch)
- Guias de distribuição para web, mobile (Android/iOS), watch
- Config mobile em `apps/mobile/apk/runtime-config.json`

## Workflows Críticos

### Desenvolvimento Local
```powershell
.\scripts\bootstrap.ps1      # Instala Rust, Node se ausentes
.\scripts\db-bootstrap.ps1   # Inicializa bancos SQLite
.\scripts\up.ps1             # Inicia nexora-core
.\scripts\healthcheck.ps1    # Verifica status (GET /health)
```

### Validação & Reset
```powershell
.\scripts\check-memory-budget.ps1    # Enforça limite 13MB
.\scripts\db-validate.ps1             # Verifica integridade
.\scripts\db-reset.ps1                # Limpa dados
.\scripts\reset.ps1                   # Reset completo
```

### Testes Remotos (Cloudflare Tunnel)
```powershell
.\scripts\remote-test-pack.ps1        # Publica + gera pack
.\scripts\revoke-remote-tester.ps1    # Revoga acesso
.\scripts\unpublish-remote.ps1        # Remove publicação
```

### Teardown
```powershell
.\scripts\down.ps1           # Para runtime
.\scripts\uninstall.ps1      # Remove binários Rust/Node
.\scripts\validate-host-internet.ps1  # Verifica conectividade
```

## Padrões & Convenções

### Versionamento
- **API**: v24.7 (header `x-api-version`)
- **Região padrão**: sa-east (env var `NEXORA_REGION`)
- **Tenant padrão**: tenant-nexora-default (env var `NEXORA_TENANT`)
- **Bind padrão**: 127.0.0.1:8080 (env var `NEXORA_BIND`)

### Estrutura de Resposta API
Todas as respostas incluem headers de contexto:
```
x-api-version: 24.7
x-region: <region_code>
x-tenant: <tenant_id>
```

Erros retornam `{"error": "...", "status": <code>}` em JSON.

### Convenções de Código Rust
- **AppConfig**: carregado de variáveis de ambiente em `AppConfig::from_env()`
- **AppState**: `Arc<AppConfig>` para estado compartilhado
- **ApiError**: tipo customizado com conversão automática para responses HTTP
- **Middlewares**: tower-http (CORS, logging, file serving)
- **Async**: tokio runtime com multi-thread

### Design Tokens & UI
- Tokens centralizados em `packages/design-tokens/tokens.json` e `tokens.css`
- UI responsiva em `core/static/` com CSS moderno
- Portais: infographic (`/infographic`), investor (`/investors`)

## Pontos de Integração

### Banco de Dados
- **Replicação**: tabelas `outbox_event` (write) / `inbox_event` (read) para CDC
- **Health**: tabela `replication_health` com registros por região
- **Expansion**: `entity_attribute_def` + `entity_attribute_value` para novos campos sem schema migration
- **Relationships**: `entity_link` para relacionamentos dinâmicos

### Observabilidade
- **Prometheus**: scrape em `infra/observability/prometheus/prometheus.yml`
- **Grafana**: dashboard JSON em `nexora-global-overview.json`
- **Tracing**: tracing-subscriber com env-filter
- **Métricas**: coletadas por tenant e região

### Autenticação & Autorização
- **Bearer tokens** em headers `Authorization`
- **Refresh tokens** via endpoint POST `/auth/refresh`
- **Role-based**: `role_code` em `AuthContext`
- **Session**: `auth_session` com expiração (usando chrono::Duration)

### Failover & Routing
- **Tenant routing policy**: define escrita em região primária
- **Regional failover**: RTO/RPO definidos, scale-out automático
- **Capacidade alvo**: 10k req/s, 1M tenants, 50k concurrent/region
- **Latência alvo**: p95 read ≤250ms, write ≤450ms

## Pontos de Partida para Novas Features

1. **Novo endpoint API**: adicione em `core/src/main.rs` no router, declare struct Input/Output, implemente lógica com contexto AuthContext
2. **Nova tabela**: adicione em `db/sql/00_schema.sql`, migre através de versionamento, documente em `data-dictionary.md`
3. **Novo design element**: adicione token em `packages/design-tokens/tokens.json`, use em `core/static/`
4. **Novo script operacional**: crie em `scripts/`, documente no README principal
5. **Novo manifesto k8s**: crie em `infra/k8s/base/`, registre em `kustomization.yaml`, crie overlay em `overlays/`

## Ambiente & Setup

- **OS**: Windows (scripts em PowerShell)
- **Rustup**: auto-instalado via `bootstrap.ps1` se ausente
- **Node.js**: auto-instalado via `bootstrap.ps1` (versão 20-lts)
- **Localhost**: http://127.0.0.1:8080
- **Manuals**: HTML em `docs/manuals/` (also generated as PDF)

## Checklist para Code Review de IA

- [ ] Respeita headers `x-api-version`, `x-region`, `x-tenant` em respostas
- [ ] Usa `AuthContext` para capturar user_id, tenant_id, role_code
- [ ] Implementa idempotência para operações críticas (checkout de outbox_event)
- [ ] ALtera de schema via SQL versionada em `db/sql/`
- [ ] Documenta novos atributos de entidade em `data-dictionary.md`
- [ ] Testa local com `scripts/healthcheck.ps1` antes de push
- [ ] Verifica memory budget `scripts/check-memory-budget.ps1` para core
- [ ] Inclui migration path para dados existentes (especialmente entity_link, entity_attribute_*)
