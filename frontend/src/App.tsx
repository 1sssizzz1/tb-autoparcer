import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowDownUp, ExternalLink, Gem, LoaderCircle, RefreshCw, Search, ShieldCheck } from "lucide-react";

type Listing = {
  listing_id: string;
  page_start: number;
  name: string;
  market_hash_name: string;
  gem: string;
  price_values: Record<string, number>;
  quantity: number;
  url: string;
};

type MarketResponse = {
  listings: Listing[];
  total_count: number;
  updated_at: string;
  complete: boolean;
  available_currencies: string[];
  stale?: boolean;
  warning?: string;
  retry_after_seconds?: number;
};

const REFRESH_INTERVAL = 5 * 60_000;
const CURRENCY_LABELS: Record<string, string> = {
  RUB: "₽ Рубли",
  USD: "$ Доллары",
  EUR: "€ Евро",
};

function App() {
  const [data, setData] = useState<MarketResponse | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [filter, setFilter] = useState("");
  const [sortAscending, setSortAscending] = useState(true);
  const [currency, setCurrency] = useState("RUB");
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
    const activeCurrency = data?.available_currencies.includes(currency)
      ? currency
      : data?.available_currencies[0] ?? currency;
    return [...(data?.listings ?? [])]
      .filter((item) => `${item.name} ${item.gem}`.toLocaleLowerCase("ru").includes(normalizedFilter))
      .sort((a, b) => sortAscending
        ? (a.price_values[activeCurrency] ?? 0) - (b.price_values[activeCurrency] ?? 0)
        : (b.price_values[activeCurrency] ?? 0) - (a.price_values[activeCurrency] ?? 0));
  }, [data, filter, sortAscending, currency]);

  const availableCurrencies = data?.available_currencies ?? [];
  const activeCurrency = availableCurrencies.includes(currency) ? currency : availableCurrencies[0] ?? currency;
  const formatPrice = (values: Record<string, number>) => {
    const value = values[activeCurrency];
    if (value === undefined) return "—";
    return new Intl.NumberFormat("ru-RU", {
      style: "currency",
      currency: activeCurrency,
      minimumFractionDigits: 2,
      maximumFractionDigits: 2,
    }).format(value / 100);
  };
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
        <div className={`live-badge ${data?.stale || (data && !data.complete) ? "stale-badge" : ""}`}><span /> {data?.stale ? "CACHED DATA" : data && !data.complete ? "PARTIAL DATA" : "LIVE MARKET"}</div>
      </header>

      <section className="hero">
        <div className="hero-copy">
          <div className="eyebrow"><span className="eyebrow-line" /> DOTA 2 / STEAM COMMUNITY MARKET</div>
          <h1>Аркана <span>Террорблейда</span></h1>
          <p>Цены в выбранной валюте и призматический самоцвет каждого лота.</p>
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
          <span className="stat-note"><span className="status-dot" /> Автообновление каждые 5 минут</span>
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
          <label className="currency-select">
            <span>Валюта</span>
            <select
              aria-label="Валюта отображения цен"
              value={activeCurrency}
              onChange={(event) => setCurrency(event.target.value)}
              disabled={availableCurrencies.length < 2}
            >
              {(availableCurrencies.length ? availableCurrencies : ["RUB"]).map((code) => (
                <option key={code} value={code}>{CURRENCY_LABELS[code] ?? code}</option>
              ))}
            </select>
          </label>
        </div>

        {error && <div className="error-banner" role="alert">
          {error}
          {refreshInterval > REFRESH_INTERVAL && ` Следующая попытка примерно через ${Math.ceil(refreshInterval / 60_000)} мин.`}
        </div>}
        {data?.warning && <div className="warning-banner" role="status">
          {data.warning}
          {data.retry_after_seconds ? ` Следующая попытка примерно через ${Math.ceil(data.retry_after_seconds / 60)} мин.` : ""}
        </div>}

        <div className="table-wrap">
          <table>
            <thead>
              <tr><th>ПРЕДМЕТ</th><th>ПРИЗМАТИЧЕСКИЙ САМОЦВЕТ</th><th>В НАЛИЧИИ</th><th>ЦЕНА</th><th /></tr>
            </thead>
            <tbody>
              {listings.map((item, index) => (
                <tr key={item.listing_id || `${item.market_hash_name}-${index}`}>
                  <td>
                    <div className="item-cell">
                      <div className={`item-icon hue-${index % 5}`}><Gem size={18} /></div>
                      <div><strong>{item.name || "Fractal Horns of Inner Abysm"}</strong><span>Лот {item.listing_id}</span></div>
                    </div>
                  </td>
                  <td><span className={`gem-tag ${item.gem === "Не указано" ? "muted" : ""}`}><span className="gem-dot" />{item.gem}</span></td>
                  <td className="quantity">{item.quantity}</td>
                  <td><strong className="price">{formatPrice(item.price_values)}</strong></td>
                  <td><a className="open-link" href={item.url} target="_blank" rel="noreferrer" title={`Открыть предложение ${item.listing_id} в Steam`} aria-label={`Перейти к лоту ${item.listing_id} в Steam Market`}>Страница в Steam <ExternalLink size={14} /></a></td>
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
          <span>Загружено лотов: {data?.listings.length ?? 0} из {data?.total_count ?? 0}</span>
          <span className="privacy"><ShieldCheck size={14} /> Только публичные данные Steam</span>
        </footer>
      </section>
      <p className="disclaimer">Пересчёт в евро, доллары и рубли выполняется по последнему доступному курсу ЦБ. Если курсы временно недоступны, остаётся валюта исходной цены Steam. Ссылка открывает страницу Steam с выбранным предложением по его ID.</p>
    </main>
  );
}

export default App;
