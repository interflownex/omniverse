import { useEffect, useMemo, useState } from 'react';

const API_BASE = (import.meta.env.VITE_NEXORA_API_BASE || 'http://localhost').replace(/\/$/, '');

const SOURCES = [
  { key: 'amazon', label: 'Amazon' },
  { key: 'alibaba', label: 'Alibaba' },
  { key: 'cj-dropshipping', label: 'CJ' },
  { key: 'aliexpress', label: 'Ali' },
  { key: 'mercadolivre', label: 'ML' },
  { key: 'shopee', label: 'Shopee' },
];

function money(cents = 0) {
  const value = Number(cents || 0) / 100;
  return new Intl.NumberFormat('pt-BR', { style: 'currency', currency: 'BRL' }).format(value);
}

async function requestJson(path, options) {
  const response = await fetch(`${API_BASE}${path}`, {
    headers: {
      accept: 'application/json',
      'content-type': 'application/json',
      ...(options?.headers || {}),
    },
    ...options,
  });
  const text = await response.text();
  let body = {};
  try {
    body = text ? JSON.parse(text) : {};
  } catch {
    body = { raw: text };
  }

  if (!response.ok) {
    throw new Error(`${response.status} ${JSON.stringify(body)}`);
  }
  return body;
}

export default function App() {
  const [activeSource, setActiveSource] = useState('amazon');
  const [category, setCategory] = useState('fitness');
  const [limit, setLimit] = useState(20);
  const [loading, setLoading] = useState(false);
  const [importing, setImporting] = useState(false);
  const [results, setResults] = useState([]);
  const [imported, setImported] = useState([]);
  const [selectedIds, setSelectedIds] = useState([]);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');

  const selectedCount = selectedIds.length;

  const canImportSelected = selectedCount > 0 && !importing;
  const canSyncAll = results.length > 0 && !importing;

  const resultMap = useMemo(() => {
    const map = new Map();
    results.forEach((item) => map.set(item.external_id, item));
    return map;
  }, [results]);

  useEffect(() => {
    void refreshImported();
  }, [activeSource]);

  async function fetchSuggestions() {
    setLoading(true);
    setError('');
    setMessage('');
    try {
      const data = await requestJson(
        `/stock/v1/products/suggestions?source=${encodeURIComponent(activeSource)}&category=${encodeURIComponent(
          category.trim() || 'trending'
        )}&limit=${encodeURIComponent(limit)}`
      );
      setResults(Array.isArray(data.data) ? data.data : []);
      setSelectedIds([]);
      setMessage(`Busca concluída em ${SOURCES.find((s) => s.key === activeSource)?.label || activeSource}.`);
    } catch (err) {
      setError(`Falha ao consultar API: ${String(err.message || err)}`);
    } finally {
      setLoading(false);
    }
  }

  async function refreshImported() {
    try {
      const data = await requestJson(
        `/stock/v1/products/imported?source=${encodeURIComponent(activeSource)}&limit=20`
      );
      setImported(Array.isArray(data.data) ? data.data : []);
    } catch {
      setImported([]);
    }
  }

  function toggleSelection(externalId) {
    setSelectedIds((current) =>
      current.includes(externalId) ? current.filter((id) => id !== externalId) : [...current, externalId]
    );
  }

  async function importSelected() {
    if (!selectedIds.length) return;
    setImporting(true);
    setError('');
    setMessage('');
    try {
      const body = await requestJson('/stock/v1/products/import-auto', {
        method: 'POST',
        body: JSON.stringify({
          source: activeSource,
          category: category.trim() || 'trending',
          limit: results.length || limit,
          selected_external_ids: selectedIds,
        }),
      });
      setMessage(`Importação seletiva concluída: ${body.count || 0} item(ns).`);
      await refreshImported();
    } catch (err) {
      setError(`Falha na importação seletiva: ${String(err.message || err)}`);
    } finally {
      setImporting(false);
    }
  }

  async function syncAll() {
    if (!results.length) return;
    setImporting(true);
    setError('');
    setMessage('');
    try {
      const body = await requestJson('/stock/v1/products/import-auto', {
        method: 'POST',
        body: JSON.stringify({
          source: activeSource,
          category: category.trim() || 'trending',
          limit: results.length,
        }),
      });
      setMessage(`Sincronização total concluída: ${body.count || 0} item(ns).`);
      await refreshImported();
    } catch (err) {
      setError(`Falha na sincronização total: ${String(err.message || err)}`);
    } finally {
      setImporting(false);
    }
  }

  return (
    <main className="app-shell">
      <section className="topbar">
        <div>
          <h1>God Mode</h1>
          <p>Painel estratégico do CEO com Dropship Manager integrado ao catálogo Nexora.</p>
        </div>
        <div className="pill">Backend: pronto</div>
      </section>

      <section className="grid grid-two">
        <article className="card">
          <h2>Dropship Manager UI</h2>
          <p className="muted">
            Busque produtos por fornecedor, selecione itens e envie direto ao catálogo da Nexora.
          </p>

          <div className="tabs" role="tablist" aria-label="Fornecedores">
            {SOURCES.map((source) => (
              <button
                key={source.key}
                className={`tab ${activeSource === source.key ? 'active' : ''}`}
                onClick={() => {
                  setActiveSource(source.key);
                  setSelectedIds([]);
                }}
              >
                {source.label}
              </button>
            ))}
          </div>

          <div className="form-row">
            <label>
              Busca por categoria
              <input
                value={category}
                onChange={(e) => setCategory(e.target.value)}
                placeholder="fitness, home, office, electronics..."
              />
            </label>

            <label>
              Limite
              <input
                type="number"
                min={1}
                max={100}
                value={limit}
                onChange={(e) => setLimit(Math.max(1, Math.min(100, Number(e.target.value) || 1)))}
              />
            </label>

            <button className="action" onClick={fetchSuggestions} disabled={loading}>
              {loading ? 'Buscando...' : 'Buscar na API'}
            </button>
          </div>

          <div className="action-row">
            <button className="action success" onClick={importSelected} disabled={!canImportSelected}>
              Importar Selecionados ({selectedCount})
            </button>
            <button className="action outline" onClick={syncAll} disabled={!canSyncAll}>
              Sincronizar Todos ({results.length})
            </button>
          </div>

          {message && <div className="banner ok">{message}</div>}
          {error && <div className="banner err">{error}</div>}

          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th />
                  <th>Título</th>
                  <th>Fonte</th>
                  <th>Custo</th>
                  <th>Frete</th>
                  <th>ID Externo</th>
                </tr>
              </thead>
              <tbody>
                {results.map((item) => {
                  const selected = selectedIds.includes(item.external_id);
                  return (
                    <tr key={`${item.source}-${item.external_id}`} className={selected ? 'selected' : ''}>
                      <td>
                        <input
                          type="checkbox"
                          checked={selected}
                          onChange={() => toggleSelection(item.external_id)}
                          aria-label={`Selecionar ${item.title}`}
                        />
                      </td>
                      <td>{item.title}</td>
                      <td>{item.source}</td>
                      <td>{money(item.cost_cents)}</td>
                      <td>{money(item.freight_cents)}</td>
                      <td>{item.external_id}</td>
                    </tr>
                  );
                })}
                {!results.length && (
                  <tr>
                    <td colSpan={6} className="empty">
                      Nenhum resultado carregado.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </article>

        <article className="card">
          <h2>Catálogo Nexora (últimos importados)</h2>
          <p className="muted">Snapshot dos produtos já sincronizados para este fornecedor.</p>

          <div className="catalog-list">
            {imported.map((item) => (
              <div className="catalog-item" key={item.product_id}>
                <div>
                  <strong>{item.title}</strong>
                  <small>{item.product_id}</small>
                </div>
                <div className="price-block">
                  <span>{money(item.final_price_cents)}</span>
                  <small>margem {money(item.margin_cents)}</small>
                </div>
              </div>
            ))}
            {!imported.length && <p className="muted">Sem itens importados para esta fonte.</p>}
          </div>

          <h3>Resumo executivo</h3>
          <ul className="summary-list">
            <li>Fornecedor ativo: {SOURCES.find((s) => s.key === activeSource)?.label || activeSource}</li>
            <li>Selecionados para importação: {selectedCount}</li>
            <li>Resultados em memória: {resultMap.size}</li>
            <li>Importados recentes: {imported.length}</li>
          </ul>
        </article>
      </section>
    </main>
  );
}
