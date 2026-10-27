import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowDownUp, ExternalLink, Gem, LoaderCircle, RefreshCw, Search, ShieldCheck } from "lucide-react";

type Listing = {
  name: string;
  market_hash_name: string;
  gem: string;
  price_text: string;
  price_cents: number;
  quantity: number;
  url: string;
};

type MarketResponse = {
  listings: Listing[];
  total_count: number;
  updated_at: string;
  stale?: boolean;
  warning?: string;
  retry_after_seconds?: number;
};

const REFRESH_INTERVAL = 60_000;

function App() {
  const [data, setData] = useState<MarketResponse | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [filter, setFilter] = useState("");
  const [sortAscending, setSortAscending] = useState(true);
  const [refreshInterval, setRefreshInterval] = useState(REFRESH_INTERVAL);
  const refreshIntervalRef = useRef(REFRESH_INTERVAL);

  const updateRefreshInterval = (interval: number) => {
    refreshIntervalRef.current = interval;
    setRefreshInterval(interval);
  };

  const refresh = useCallback(async (force = false) => {
    setLoading(true);
    setError("");
    try {
      const response = await fetch(force ? "/api/listings?force=1" : "/api/listings");
      if (!response.ok) {
        const body = await response.json().catch(() => null) as { error?: string; retry_after_seconds?: number; source?: string } | null;
        console.error("[TB Market Watch] API request failed", {
          status: response.status,
          source: body?.source ?? "unknown",
          message: body?.error ?? "No error message in API response",
          retryAfterSeconds: body?.retry_after_seconds,
        });
        if (body?.retry_after_seconds) {
          updateRefreshInterval(body.retry_after_seconds * 1000);
        }
        setError(body?.error || `Ошибка сервера (${response.status})`);
        return;
      }
      const result = await response.json() as MarketResponse;
      if (result.stale || result.listings.length === 0) {
        console.warn("[TB Market Watch] API returned no current listings", {
          stale: result.stale ?? false,
          listingCount: result.listings.length,
          totalCount: result.total_count,
          warning: result.warning,
          updatedAt: result.updated_at,
        });
      }
      setData(result);
      updateRefreshInterval(result.retry_after_seconds ? result.retry_after_seconds * 1000 : REFRESH_INTERVAL);
    } catch (reason) {
      console.error("[TB Market Watch] Could not reach listings API", reason);
      setError(reason instanceof Error ? reason.message : "Не удалось загрузить рынок");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    let timer: number;
    let cancelled = false;
    const poll = async () => {
      await refresh();
      if (!cancelled) {
        timer = window.setTimeout(poll, refreshIntervalRef.current);
      }
    };
    void poll();
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [refresh]);

  const listings = useMemo(() => {
    const normalizedFilter = filter.trim().toLocaleLowerCase("ru");
    return [...(data?.listings ?? [])]
      .filter((item) => `${item.name} ${item.gem}`.toLocaleLowerCase("ru").includes(normalizedFilter))
      .sort((a, b) => sortAscending ? a.price_cents - b.price_cents : b.price_cents - a.price_cents);
  }, [data, filter, sortAscending]);

  const uniqueGems = new Set((data?.listings ?? []).map((item) => item.gem).filter((gem) => gem !== "Не указано")).size;
  const updatedAt = data?.updated_at
    ? new Intl.DateTimeFormat("ru-RU", { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(data.updated_at))
    : "—";

  return (
    <main className="shell">
      <header className="topbar">
        <a className="brand" href="#" aria-label="TB Market Watch">
          <span className="brand-mark"><Gem size={19} /></span>
          <span>TB <b>MARKET WATCH</b></span>
        </a>
        <div className={`live-badge ${data?.stale ? "stale-badge" : ""}`}><span /> {data?.stale ? "CACHED DATA" : "LIVE MARKET"}</div>
      </header>

      <section className="hero">
        <div className="hero-copy">
          <div className="eyebrow"><span className="eyebrow-line" /> DOTA 2 / STEAM COMMUNITY MARKET</div>
          <h1>Аркана <span>Террорблейда</span></h1>
          <p>Цены и призматический самоцвет арканы Fractal Horns of Inner Abysm, если Steam указывает его в данных лота.</p>
        </div>
        <div className="hero-item" aria-hidden="true">
          <div className="orb orb-one" />
          <div className="orb orb-two" />
          <div className="item-emblem"><Gem size={58} strokeWidth={1.2} /></div>
          <div className="item-caption">DEMON EATER <span>ARCANA</span></div>
        </div>
      </section>

      <section className="stats" aria-label="Сводка рынка">
        <article className="stat-card">
          <span className="stat-label">АКТИВНЫЕ ЛОТЫ</span>
          <strong>{data?.total_count ?? "—"}</strong>
          <span className="stat-note">Fractal Horns of Inner Abysm</span>
        </article>
        <article className="stat-card">
          <span className="stat-label">РАСПОЗНАНО САМОЦВЕТОВ</span>
          <strong>{data ? uniqueGems : "—"}</strong>
          <span className="stat-note">в публичных данных Steam</span>
        </article>
        <article className="stat-card stat-update">
          <span className="stat-label">ПОСЛЕДНЕЕ ОБНОВЛЕНИЕ</span>
          <strong>{updatedAt}</strong>
          <span className="stat-note"><span className="status-dot" /> Автообновление каждую минуту</span>
        </article>
      </section>

      <section className="market-panel">
        <div className="panel-heading">
          <div>
            <div className="section-kicker">ТОРГОВАЯ ПЛОЩАДКА</div>
            <h2>Предложения <span>{data?.total_count ?? 0}</span></h2>
          </div>
          <button className="refresh-button" onClick={() => void refresh(true)} disabled={loading}>
            {loading ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}
            {loading ? "Обновление" : "Обновить"}
          </button>
        </div>

        <div className="toolbar">
          <label className="searchbox">
            <Search size={17} />
            <input
              aria-label="Поиск по названию или самоцвету"
              placeholder="Поиск по названию или самоцвету"
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
            />
          </label>
          <button className="sort-button" onClick={() => setSortAscending((current) => !current)}>
            <ArrowDownUp size={15} /> Цена {sortAscending ? "↑" : "↓"}
          </button>
        </div>

        {error && <div className="error-banner" role="alert">
          {error}
          {refreshInterval > REFRESH_INTERVAL && ` Следующая попытка примерно через ${Math.ceil(refreshInterval / 60_000)} мин.`}
        </div>}
        {data?.warning && <div className="warning-banner" role="status">{data.warning} Следующая попытка примерно через {Math.ceil((data.retry_after_seconds ?? 60) / 60)} мин.</div>}

        <div className="table-wrap">
          <table>
            <thead>
              <tr><th>ПРЕДМЕТ</th><th>ПРИЗМАТИЧЕСКИЙ САМОЦВЕТ</th><th>В НАЛИЧИИ</th><th>ЦЕНА</th><th /></tr>
            </thead>
            <tbody>
              {listings.map((item, index) => (
                <tr key={`${item.market_hash_name}-${index}`}>
                  <td>
                    <div className="item-cell">
                      <div className={`item-icon hue-${index % 5}`}><Gem size={18} /></div>
                      <div><strong>{item.name || "Fractal Horns of Inner Abysm"}</strong><span>Dota 2 · Аркана</span></div>
                    </div>
                  </td>
                  <td><span className={`gem-tag ${item.gem === "Не указано" ? "muted" : ""}`}><span className="gem-dot" />{item.gem}</span></td>
                  <td className="quantity">{item.quantity}</td>
                  <td><strong className="price">{item.price_text || "—"}</strong></td>
                  <td><a className="open-link" href={item.url} target="_blank" rel="noreferrer" aria-label={`Открыть ${item.name} в Steam Market`}><ExternalLink size={16} /></a></td>
                </tr>
              ))}
              {listings.length === 0 && !loading && !error && (
                <tr><td className="empty-state" colSpan={5}>{data ? "Лоты не найдены. Попробуйте изменить поиск." : "Загрузка лотов Steam Market…"}</td></tr>
              )}
              {loading && !data && (
                <tr><td className="empty-state" colSpan={5}><LoaderCircle className="spin" size={18} /> Подключаемся к Steam Market…</td></tr>
              )}
            </tbody>
          </table>
        </div>
        <footer className="panel-footer">
          <span>Показано предметов: {listings.length}</span>
          <span className="privacy"><ShieldCheck size={14} /> Только публичные данные Steam</span>
        </footer>
      </section>
      <p className="disclaimer">Steam Market объединяет аркану по названию и может не передавать сведения о самоцвете. Цены и наличие могут измениться.</p>
    </main>
  );
}

export default App;
