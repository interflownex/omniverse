const moduleCatalog = [
  { code: "auth", name: "IAM/Auth", desc: "Autenticação, sessões, RBAC e trilha de auditoria.", profit: "+18% MRR por redução de fraude" },
  { code: "users", name: "Usuários & Perfis", desc: "Gestão unificada de PF/PJ com compliance.", profit: "+12% retenção por onboarding sem atrito" },
  { code: "catalog", name: "Catálogo de Serviços", desc: "Serviços versionados com SLA e política global.", profit: "+20% upsell por pacotes" },
  { code: "workflow", name: "Workflow de Solicitações", desc: "Abertura, escalonamento e resolução em etapas.", profit: "-30% custo operacional" },
  { code: "notify", name: "Notificações", desc: "Eventos in-app com sincronização web/mobile/watch.", profit: "+15% eficiência do time" },
  { code: "billing", name: "Billing", desc: "Faturamento sandbox com trilha fiscal e integração.", profit: "+22% previsibilidade de caixa" },
  { code: "analytics", name: "Analytics", desc: "Visão de SLA, receita e filas por tenant/região.", profit: "+14% margem por decisões de dados" },
  { code: "config", name: "Configuração Tenant", desc: "Timezone, moeda, roteamento e políticas de residência.", profit: "+17% expansão global" }
];

function currency(v) {
  const n = Number(v || 0);
  return new Intl.NumberFormat("pt-BR", { style: "currency", currency: "BRL" }).format(n);
}

async function loadOverview() {
  const loginResp = await fetch("/api/v24.8/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ email: "admin@nexora.local", password: "admin247" })
  });
  const login = await loginResp.json();
  const token = login.access_token;

  const analyticsResp = await fetch("/api/v24.8/analytics/overview", {
    headers: { Authorization: `Bearer ${token}` }
  });
  const analytics = await analyticsResp.json();

  const routingResp = await fetch("/api/v24.8/platform/routing");
  const routing = await routingResp.json();

  document.getElementById("kpiOpen").textContent = analytics.open_requests ?? 0;
  document.getElementById("kpiNotif").textContent = analytics.unread_notifications ?? 0;
  document.getElementById("kpiRevenue").textContent = currency(analytics.total_invoiced);
  document.getElementById("kpiRegion").textContent = routing.routing?.[0]?.write_region ?? "n/a";
}

function renderModules() {
  const root = document.getElementById("moduleGrid");
  moduleCatalog.forEach((m) => {
    const el = document.createElement("article");
    el.className = "module-card";
    el.innerHTML = `
      <h3>${m.name}</h3>
      <p>${m.desc}</p>
      <div class="profit">Rentabilidade estimada: ${m.profit}</div>
    `;
    root.appendChild(el);
  });
}

function setLocaleTexts(locale) {
  const map = {
    "pt-BR": {
      title: "Centro de Operações NEXORA",
      text: "Arquitetura global pronta para escala, mantendo perfil local ultraleve para validação e testes."
    },
    "en-US": {
      title: "NEXORA Operations Center",
      text: "Global-scale architecture with a lightweight local profile for validation and testing."
    },
    "es-ES": {
      title: "Centro de Operaciones NEXORA",
      text: "Arquitectura global preparada para escalar con perfil local ultraligero para pruebas."
    }
  };
  const t = map[locale] || map["pt-BR"];
  document.getElementById("heroTitle").textContent = t.title;
  document.getElementById("heroText").textContent = t.text;
}

(async function init() {
  renderModules();
  setLocaleTexts("pt-BR");
  try {
    await loadOverview();
  } catch (_) {
    document.getElementById("kpiOpen").textContent = "--";
    document.getElementById("kpiNotif").textContent = "--";
    document.getElementById("kpiRevenue").textContent = "--";
    document.getElementById("kpiRegion").textContent = "--";
  }
})();
