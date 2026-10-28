import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowDownUp, ExternalLink, Gem, LoaderCircle, RefreshCw, Search, ShieldCheck, Sparkles } from "lucide-react";

type Listing = {
  listing_id: string;
  page_start: number;
  name: string;
  market_hash_name: string;
  market_source: string;
  source_item_id?: string;
  gem: string;
  price_values: Record<string, number>;
  autobuy_price_values?: Record<string, number>;
  quantity: number;
  url: string;
};

type MarketResponse = {
  listings: Listing[];
  total_count: number;
  steam_total_count: number;
  dota2net_total_count: number;
  updated_at: string;
  complete: boolean;
  available_currencies: string[];
  stale?: boolean;
  warning?: string;
  retry_after_seconds?: number;
};

type GemPriceComparison = {
  gem: string;
  steam?: number;
  dota2net?: number;
  dota2netAutoBuy?: number;
  spread?: number;
  spreadPercent?: number;
  cheaperSource?: string;
};

type GemTone = "blue" | "green" | "gold" | "orange" | "purple" | "red" | "pink" | "grey";

const REFRESH_INTERVAL = 5 * 60_000;
const CURRENCY_LABELS: Record<string, string> = {
  RUB: "₽ Рубли",
  USD: "$ Доллары",
  EUR: "€ Евро",
};

function getSourceLabel(source: string): string {
  return source === "Steam Market" ? "Steam" : "Dota2.net";
}

function getGemTone(gem: string): GemTone {
  const name = gem.toLocaleLowerCase("en");
  if (name === "не указано" || /grey|gray|black/.test(name)) return "grey";
  if (/blue|ships in the night/.test(name)) return "blue";
  if (/green|ground|tnim s'nnam/.test(name)) return "green";
  if (/gold|beige|earth|creator's light/.test(name)) return "gold";
  if (/orange|summer warmth|explosive burst/.test(name)) return "orange";
  if (/red|rubiline|vermillion|renewal/.test(name)) return "red";
  if (/plushy shag/.test(name)) return "pink";
  if (/purple|shade|dungeon doom/.test(name)) return "purple";
  return "grey";
}

function formatOfferCount(count: number): string {
  const lastTwo = count % 100;
  const last = count % 10;
  if (lastTwo >= 11 && lastTwo <= 14) return `${count} лотов`;
  if (last === 1) return `${count} лот`;
  if (last >= 2 && last <= 4) return `${count} лота`;
  return `${count} лотов`;
}

function formatCurrencyValue(value: number, currency: string): string {
  return new Intl.NumberFormat("ru-RU", {
    style: "currency",
    currency,
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(value / 100);
}

function App() {
  const [data, setData] = useState<MarketResponse | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [filter, setFilter] = useState("");
  const [selectedGem, setSelectedGem] = useState("");
  const [sortAscending, setSortAscending] = useState(true);
  const [currency, setCurrency] = useState("RUB");
  const [activeTab, setActiveTab] = useState<"catalog" | "comparison">("catalog");
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
      .filter((item) =>
        (!selectedGem || item.gem === selectedGem) &&
        `${item.name} ${item.gem}`.toLocaleLowerCase("ru").includes(normalizedFilter))
      .sort((a, b) => sortAscending
        ? (a.price_values[activeCurrency] ?? 0) - (b.price_values[activeCurrency] ?? 0)
        : (b.price_values[activeCurrency] ?? 0) - (a.price_values[activeCurrency] ?? 0));
  }, [data, filter, selectedGem, sortAscending, currency]);

  const availableCurrencies = data?.available_currencies ?? [];
  const activeCurrency = availableCurrencies.includes(currency) ? currency : availableCurrencies[0] ?? currency;
  const formatPrice = (values: Record<string, number>) => {
    const value = values[activeCurrency];
    if (value === undefined) return "—";
    return formatCurrencyValue(value, activeCurrency);
  };
  const uniqueGems = new Set((data?.listings ?? []).map((item) => item.gem).filter((gem) => gem !== "Не указано")).size;
  const gemOptions = [...new Set((data?.listings ?? []).map((item) => item.gem))]
    .sort((a, b) => a.localeCompare(b, "ru"));
  const gemSummaries = useMemo(() => {
    const summaries = new Map<string, { count: number; lowestPrice?: number }>();
    for (const item of data?.listings ?? []) {
      const summary = summaries.get(item.gem) ?? { count: 0 };
      summary.count += 1;
      const price = item.price_values[activeCurrency];
      if (price !== undefined && (summary.lowestPrice === undefined || price < summary.lowestPrice)) {
        summary.lowestPrice = price;
      }
      summaries.set(item.gem, summary);
    }
    return [...summaries.entries()]
      .map(([gem, summary]) => ({ gem, ...summary }))
      .sort((a, b) => {
        if (a.gem === "Не указано") return 1;
        if (b.gem === "Не указано") return -1;
        return (a.lowestPrice ?? Number.MAX_SAFE_INTEGER) - (b.lowestPrice ?? Number.MAX_SAFE_INTEGER);
      });
  }, [data, activeCurrency]);
  const gemPriceComparisons = useMemo(() => {
    const prices = new Map<string, { steam?: number; dota2net?: number; dota2netAutoBuy?: number }>();
    for (const item of data?.listings ?? []) {
      if (item.gem === "Не указано") continue;
      const price = item.price_values[activeCurrency];
      const summary = prices.get(item.gem) ?? {};
      if (price !== undefined && item.market_source === "Steam Market") {
        summary.steam = summary.steam === undefined ? price : Math.min(summary.steam, price);
      } else if (price !== undefined && item.market_source === "market.dota2.net") {
        summary.dota2net = summary.dota2net === undefined ? price : Math.min(summary.dota2net, price);
      }
      const autoBuyPrice = item.autobuy_price_values?.[activeCurrency];
      if (item.market_source === "market.dota2.net" && autoBuyPrice !== undefined) {
        summary.dota2netAutoBuy = summary.dota2netAutoBuy === undefined
          ? autoBuyPrice
          : Math.max(summary.dota2netAutoBuy, autoBuyPrice);
      }
      prices.set(item.gem, summary);
    }
    return [...prices.entries()]
      .map(([gem, marketPrices]): GemPriceComparison => {
        const { steam, dota2net } = marketPrices;
        if (steam === undefined || dota2net === undefined) {
          return { gem, steam, dota2net, dota2netAutoBuy: marketPrices.dota2netAutoBuy };
        }
        const spread = Math.abs(steam - dota2net);
        const lowerPrice = Math.min(steam, dota2net);
        return {
          gem,
          steam,
          dota2net,
          dota2netAutoBuy: marketPrices.dota2netAutoBuy,
          spread,
          spreadPercent: lowerPrice > 0 ? (spread / lowerPrice) * 100 : 0,
          cheaperSource: steam === dota2net ? undefined : steam < dota2net ? "Steam" : "Dota2.net",
        };
      })
      .sort((a, b) => (b.spreadPercent ?? -1) - (a.spreadPercent ?? -1) || a.gem.localeCompare(b.gem));
  }, [data, activeCurrency]);
  const updatedAt = data?.updated_at
    ? new Intl.DateTimeFormat("ru-RU", { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(data.updated_at))
    : "—";

  const selectGem = (gem: string) => {
    setSelectedGem((current) => current === gem ? "" : gem);
    setFilter("");
    window.setTimeout(() => document.getElementById("listings")?.scrollIntoView({ behavior: "smooth", block: "start" }), 0);
  };

  return (
    <main className="catalog-shell">
      <header className="topbar">
        <a className="brand" href="#" aria-label="TB Market Watch">
          <span className="brand-mark"><Gem size={19} /></span>
          <span>TB <b>MARKET WATCH</b></span>
        </a>
        <nav className="catalog-nav" aria-label="Навигация">
          <button className={activeTab === "catalog" ? "nav-active" : ""} type="button" onClick={() => setActiveTab("catalog")} aria-current={activeTab === "catalog" ? "page" : undefined}>Каталог</button>
          <button className={activeTab === "comparison" ? "nav-active" : ""} type="button" onClick={() => setActiveTab("comparison")} aria-current={activeTab === "comparison" ? "page" : undefined}>Сравнение цен</button>
        </nav>
        <div className={`live-badge ${data?.stale || (data && !data.complete) ? "stale-badge" : ""}`}><span /> {data?.stale ? "КЭШ" : data && !data.complete ? "ЧАСТИЧНЫЕ ДАННЫЕ" : "STEAM + DOTA2.NET"}</div>
      </header>

      <section className="catalog-hero">
        <div className="hero-copy">
          <div className="eyebrow"><span className="eyebrow-line" /> DOTA 2 <span className="crumb-slash">/</span> КАТАЛОГ ГЕРОЯ</div>
          <h1>Аркана <span>Террорблейда</span></h1>
          <p>Fractal Horns of Inner Abysm — предложения Steam и Dota2.net с ценами и призматическими самоцветами.</p>
          <div className="hero-pills">
            <span><Sparkles size={14} /> ARCANA</span>
            <span><Gem size={14} /> {data?.total_count ?? "—"} предложений</span>
          </div>
        </div>
        <div className="hero-item" aria-hidden="true">
          <div className="orb orb-one" />
          <div className="orb orb-two" />
          <div className="hero-art-glow" />
          <div className="item-emblem"><Gem size={64} strokeWidth={1.1} /></div>
          <div className="item-caption">FRACTAL HORNS <span>ARCANA</span></div>
        </div>
      </section>

      {activeTab === "catalog" ? <>
      <section className="catalog-section gem-overview" id="catalog">
        <div className="panel-heading">
          <div>
            <div className="section-kicker">ВЫБЕРИТЕ ПРИЗМАТИЧЕСКИЙ САМОЦВЕТ</div>
            <h2>Самоцветы <span>{gemSummaries.length} категорий · цены от минимума на рынке</span></h2>
          </div>
          <div className="catalog-meta">
            <span className="last-updated">Обновлено: {updatedAt}</span>
            <button className="refresh-button" onClick={() => void refresh(true)} disabled={loading}>
              {loading ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}
              {loading ? "Обновление" : "Обновить"}
            </button>
          </div>
        </div>

        <div className="gem-category-grid">
          {gemSummaries.map(({ gem, count, lowestPrice }) => (
            <button
              className={`gem-category-card gem-tone-${getGemTone(gem)} ${selectedGem === gem ? "selected" : ""}`}
              key={gem}
              type="button"
              aria-pressed={selectedGem === gem}
              onClick={() => selectGem(gem)}
            >
              <span className="category-gem-icon"><Gem size={27} strokeWidth={1.6} /></span>
              <span className="category-gem-name">{gem}</span>
              <span className="category-market">
                <span className="category-price">{lowestPrice === undefined ? "—" : `от ${formatPrice({ [activeCurrency]: lowestPrice })}`}</span>
                <span className="category-count">{formatOfferCount(count)}</span>
              </span>
              <span className="category-arrow" aria-hidden="true">↗</span>
            </button>
          ))}
          {gemSummaries.length === 0 && !loading && (
            <div className="empty-state">{data ? "Самоцветы не найдены." : "Загружаем категории самоцветов…"}</div>
          )}
        </div>
        <footer className="panel-footer">
          <span>{selectedGem ? `Выбран самоцвет: ${selectedGem}` : "Нажмите на карточку, чтобы посмотреть лоты с этим самоцветом"}</span>
          <button className="clear-gem-button" type="button" onClick={() => { setSelectedGem(""); setFilter(""); }}>
            Все предложения
          </button>
        </footer>
      </section>

      <section className="catalog-section listings-section" id="listings">
        <div className="panel-heading">
          <div>
            <div className="section-kicker">{selectedGem ? `САМОЦВЕТ: ${selectedGem.toLocaleUpperCase("ru")}` : "АКТУАЛЬНЫЕ ПРЕДЛОЖЕНИЯ"}</div>
            <h2>Лоты <span>{listings.length} предложений</span></h2>
          </div>
          <span className="last-updated">{data
            ? `Steam: ${data.steam_total_count} · Dota2.net: ${data.dota2net_total_count}`
            : "Загружаем рынок…"}</span>
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
          <div className="filter-controls">
            <label className="catalog-select">
              <span>Самоцвет</span>
              <select
                aria-label="Фильтр по самоцвету"
                value={selectedGem}
                onChange={(event) => setSelectedGem(event.target.value)}
              >
                <option value="">Все самоцветы</option>
                {gemOptions.map((gem) => (
                  <option key={gem} value={gem}>{gem}</option>
                ))}
              </select>
            </label>
            <label className="catalog-select">
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
            <button className="sort-button" onClick={() => setSortAscending((current) => !current)}>
              <ArrowDownUp size={15} /> Цена {sortAscending ? "↑" : "↓"}
            </button>
          </div>
        </div>

        {error && <div className="error-banner" role="alert">
          {error}
          {refreshInterval > REFRESH_INTERVAL && ` Следующая попытка примерно через ${Math.ceil(refreshInterval / 60_000)} мин.`}
        </div>}
        {data?.warning && <div className="warning-banner" role="status">
          {data.warning}
          {data.retry_after_seconds ? ` Следующая попытка примерно через ${Math.ceil(data.retry_after_seconds / 60)} мин.` : ""}
        </div>}

        <div className="catalog-grid">
          {listings.map((item, index) => {
            const sourceLabel = getSourceLabel(item.market_source);
            const sourceItemId = item.source_item_id || item.listing_id;
            return (
            <article className="market-card" key={item.listing_id || `${item.market_hash_name}-${index}`}>
              <div className={`card-art gem-tone-${getGemTone(item.gem)}`} aria-hidden="true">
                <div className="art-aura" />
                <div className="art-orbit art-orbit-one" />
                <div className="art-orbit art-orbit-two" />
                <div className="art-gem"><Gem size={66} strokeWidth={1.1} /></div>
                <span className="art-label">TERRORBLADE <b>ARCANA</b></span>
                <span className="art-sequence">{String(index + 1).padStart(2, "0")}</span>
              </div>
              <div className="card-content">
                <div className="card-title-row">
                  <div>
                    <span className="card-kicker">FRACTAL HORNS OF</span>
                    <h3>{item.name || "Inner Abysm"}</h3>
                  </div>
                  <div className="listing-tags">
                    <span className={`gem-tag ${item.gem === "Не указано" ? "muted" : ""}`}><span className="gem-dot" />{item.gem}</span>
                    <span className={`source-tag ${item.market_source === "Steam Market" ? "steam-source" : "dota-source"}`}>{sourceLabel}</span>
                  </div>
                </div>
                <div className="card-bottom">
                  <div className="card-price"><span>ЦЕНА</span><strong>{formatPrice(item.price_values)}</strong></div>
                  <a className="open-link" href={item.url} target="_blank" rel="noreferrer" title={`Открыть предложение ${sourceItemId} на ${sourceLabel}`} aria-label={`Перейти к лоту ${sourceItemId} на ${sourceLabel}`}>
                    {sourceLabel} <ExternalLink size={14} />
                  </a>
                </div>
                <div className="card-id"><span>ЛОТ</span> {sourceItemId}</div>
              </div>
            </article>
            );
          })}
          {listings.length === 0 && !loading && !error && (
            <div className="empty-state">{data ? "Предложения не найдены. Измените фильтры." : "Лоты не найдены."}</div>
          )}
          {loading && !data && (
            <div className="empty-state"><LoaderCircle className="spin" size={18} /> Загружаем предложения маркетплейсов…</div>
          )}
        </div>
        <footer className="panel-footer">
          <span>Показано {listings.length} из {data?.listings.length ?? 0} загруженных лотов · {uniqueGems} разных самоцветов</span>
          <span className="privacy"><ShieldCheck size={14} /> Только публичные данные маркетплейсов</span>
        </footer>
      </section>
      <p className="disclaimer">Пересчёт в евро, доллары и рубли выполняется по последнему доступному курсу ЦБ. Ссылка на карточке открывает выбранное предложение в Steam Market.</p>
      </> : (
        <section className="catalog-section comparison-section" aria-labelledby="comparison-title">
          <div className="panel-heading comparison-heading">
            <div>
              <div className="section-kicker">МИНИМАЛЬНЫЕ ЦЕНЫ ПО ПРИЗМАТИЧЕСКИМ САМОЦВЕТАМ</div>
              <h2 id="comparison-title">Сравнение площадок <span>{gemPriceComparisons.length} гемов</span></h2>
            </div>
            <div className="comparison-controls">
              <span className="last-updated">Обновлено: {updatedAt}</span>
              <label className="catalog-select">
                <span>Валюта</span>
                <select
                  aria-label="Валюта сравнения"
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
          </div>
          {error && <div className="error-banner" role="alert">{error}</div>}
          {data?.warning && <div className="warning-banner" role="status">{data.warning}</div>}
          <div className="comparison-table-wrap">
            <table className="comparison-table">
              <thead>
                <tr>
                  <th>Самоцвет</th>
                  <th>Steam · минимум</th>
                  <th>Dota2.net · минимум</th>
                  <th>Dota2.net · автопокупка</th>
                  <th>Разница</th>
                </tr>
              </thead>
              <tbody>
                {gemPriceComparisons.map((row) => (
                  <tr key={row.gem}>
                    <th scope="row">
                      <span className={`comparison-gem gem-tone-${getGemTone(row.gem)}`}>
                        <Gem size={16} /> {row.gem}
                      </span>
                    </th>
                    <td>{row.steam === undefined ? <span className="comparison-missing">Нет лотов</span> : formatCurrencyValue(row.steam, activeCurrency)}</td>
                    <td>{row.dota2net === undefined ? <span className="comparison-missing">Нет лотов</span> : formatCurrencyValue(row.dota2net, activeCurrency)}</td>
                    <td>
                      {row.dota2netAutoBuy === undefined
                        ? <span className="comparison-missing">Нет заявок</span>
                        : <div className="comparison-difference">
                          <strong className="comparison-autobuy-price">{formatCurrencyValue(row.dota2netAutoBuy, activeCurrency)}</strong>
                          <small>Максимальная текущая цена</small>
                        </div>}
                    </td>
                    <td>
                      {row.spread === undefined ? <span className="comparison-missing">Недостаточно данных</span> : (
                        <div className="comparison-difference">
                          <strong>{formatCurrencyValue(row.spread, activeCurrency)} <span>({row.spreadPercent?.toFixed(1)}%)</span></strong>
                          <small>{row.cheaperSource ? `Дешевле на ${row.cheaperSource}` : "Одинаковая цена"}</small>
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
                {gemPriceComparisons.length === 0 && (
                  <tr><td colSpan={5} className="comparison-empty">{data ? "Нет данных для сравнения по гему." : "Загружаем предложения…"}</td></tr>
                )}
              </tbody>
            </table>
          </div>
          <footer className="panel-footer">
            <span>Разница считается между минимальными ценами. Процент рассчитан относительно более низкой цены.</span>
            <button className="refresh-button" type="button" onClick={() => void refresh(true)} disabled={loading}>
              {loading ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}
              {loading ? "Обновление" : "Обновить"}
            </button>
          </footer>
        </section>
      )}
    </main>
  );
}

export default App;
