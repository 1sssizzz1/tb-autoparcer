package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	steamMarketURL      = "https://steamcommunity.com/market/actions"
	steamListingPageURL = "https://steamcommunity.com/market/listings/570/Fractal%20Horns%20of%20Inner%20Abysm?category_Hero=npc_dota_hero_terrorblade&category_Rarity=Rarity_Arcana"
	cbrRatesURL         = "https://www.cbr-xml-daily.ru/daily_json.js"
	steamAppID          = 570
	searchQuery         = "Fractal Horns of Inner Abysm"
	itemHashName        = "Fractal Horns of Inner Abysm"
	cacheDuration       = 5 * time.Minute
	ratesCacheDuration  = 12 * time.Hour
	maxMarketListings   = 2000
)

type listing struct {
	ListingID      string           `json:"listing_id"`
	PageStart      int              `json:"page_start"`
	Name           string           `json:"name"`
	MarketHashName string           `json:"market_hash_name"`
	Gem            string           `json:"gem"`
	PriceValues    map[string]int64 `json:"price_values"`
	Quantity       int              `json:"quantity"`
	URL            string           `json:"url"`
}

type priceResponse struct {
	Listings            []listing `json:"listings"`
	TotalCount          int       `json:"total_count"`
	UpdatedAt           time.Time `json:"updated_at"`
	Complete            bool      `json:"complete"`
	AvailableCurrencies []string  `json:"available_currencies"`
	Stale               bool      `json:"stale,omitempty"`
	Warning             string    `json:"warning,omitempty"`
	RetryAfterSeconds   int       `json:"retry_after_seconds,omitempty"`
}

type exchangeRateResponse struct {
	Valute map[string]struct {
		Nominal float64 `json:"Nominal"`
		Value   float64 `json:"Value"`
	} `json:"Valute"`
}

func (c *marketClient) getExchangeRates(ctx context.Context) (map[string]float64, error) {
	if time.Now().Before(c.ratesExpires) && len(c.rates) > 0 {
		return c.rates, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ratesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create exchange-rate request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.ratesClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request exchange rates: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exchange-rate provider returned HTTP %d", resp.StatusCode)
	}
	var data exchangeRateResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode exchange rates: %w", err)
	}
	rates := make(map[string]float64, len(data.Valute))
	for code, value := range data.Valute {
		if value.Nominal > 0 && value.Value > 0 {
			rates[code] = value.Value / value.Nominal
		}
	}
	if len(rates) == 0 {
		return nil, fmt.Errorf("exchange-rate provider returned no usable rates")
	}
	c.rates = rates
	c.ratesExpires = time.Now().Add(ratesCacheDuration)
	return rates, nil
}

var steamCurrencyCodes = map[int]string{
	1: "USD", 2: "GBP", 3: "EUR", 4: "CHF", 5: "RUB",
	6: "PLN", 7: "BRL", 8: "JPY", 9: "NOK", 10: "IDR",
	11: "MYR", 12: "PHP", 13: "SGD", 14: "THB", 15: "VND",
	16: "KRW", 17: "TRY", 18: "UAH", 19: "MXN", 20: "CAD",
	21: "AUD", 22: "NZD", 23: "CNY", 24: "INR", 25: "CLP",
	26: "PEN", 27: "COP", 28: "ZAR", 29: "HKD", 30: "TWD",
	31: "SAR", 32: "AED", 33: "ARS", 34: "ILS", 35: "KZT",
}

func convertPriceToRUB(priceMinor int64, currency int, rates map[string]float64) (int64, error) {
	if priceMinor < 0 {
		return 0, fmt.Errorf("negative price")
	}
	if currency == 5 {
		return priceMinor, nil
	}
	code, ok := steamCurrencyCodes[currency]
	if !ok {
		return 0, fmt.Errorf("unsupported Steam currency ID %d", currency)
	}
	rate, ok := rates[code]
	if !ok || rate <= 0 {
		return 0, fmt.Errorf("no RUB exchange rate available for %s", code)
	}
	decimalPlaces := 2
	switch currency {
	case 8, 10, 15, 16, 25:
		decimalPlaces = 0
	}
	converted := float64(priceMinor) * rate * 100 / math.Pow10(decimalPlaces)
	if converted > float64(math.MaxInt64) {
		return 0, fmt.Errorf("converted price overflows")
	}
	return int64(math.Round(converted)), nil
}

func convertPriceValues(priceMinor int64, steamCurrency int, rates map[string]float64) map[string]int64 {
	code, ok := steamCurrencyCodes[steamCurrency]
	if !ok || priceMinor < 0 {
		return nil
	}
	decimalPlaces := 2
	switch steamCurrency {
	case 8, 10, 15, 16, 25:
		decimalPlaces = 0
	}
	nativeAmount := float64(priceMinor) / math.Pow10(decimalPlaces)
	values := make(map[string]int64, 3)
	targets := []string{"RUB", "USD", "EUR"}
	for _, target := range targets {
		var nativeRate float64
		if code == "RUB" {
			nativeRate = 1
		} else {
			nativeRate = rates[code]
		}
		var targetRate float64
		if target == "RUB" {
			targetRate = 1
		} else {
			targetRate = rates[target]
		}
		if nativeRate <= 0 || targetRate <= 0 {
			continue
		}
		values[target] = int64(math.Round(nativeAmount * nativeRate / targetRate * 100))
	}
	if _, ok := values[code]; !ok {
		values[code] = int64(math.Round(nativeAmount * 100))
	}
	return values
}

func orderedAvailableCurrencies(prices map[string]int64) []string {
	currencies := make([]string, 0, len(prices))
	for _, currency := range []string{"RUB", "USD", "EUR"} {
		if _, ok := prices[currency]; ok {
			currencies = append(currencies, currency)
		}
	}
	return currencies
}

func intersectCurrencies(current []string, prices map[string]int64) []string {
	filtered := current[:0]
	for _, currency := range current {
		if _, ok := prices[currency]; ok {
			filtered = append(filtered, currency)
		}
	}
	return filtered
}

func formatRUB(kopecks int64) string {
	rubles, fraction := kopecks/100, kopecks%100
	if fraction < 0 {
		fraction = -fraction
	}
	value := strconv.FormatInt(rubles, 10)
	for i := len(value) - 3; i > 0; i -= 3 {
		value = value[:i] + "\u00a0" + value[i:]
	}
	return fmt.Sprintf("%s,%02d ₽", value, fraction)
}

func listingURL(listingID string) string {
	base := "https://steamcommunity.com/market/listings/" + fmt.Sprint(steamAppID) + "/" + url.PathEscape(itemHashName) +
		"?category_Hero=npc_dota_hero_terrorblade&category_Rarity=Rarity_Arcana"
	if listingID != "" {
		escapedID := url.QueryEscape(listingID)
		base += "&listingid=" + escapedID + "&detail=" + escapedID
	}
	return base
}

func repairCachedListingURLs(response *priceResponse) bool {
	changed := false
	for i := range response.Listings {
		item := &response.Listings[i]
		if item.URL != listingURL(item.ListingID) {
			item.URL = listingURL(item.ListingID)
			changed = true
		}
	}
	return changed
}

type steamMarketPage struct {
	Data struct {
		More       bool                 `json:"more"`
		Start      int                  `json:"start"`
		TotalCount int                  `json:"total_count"`
		Listings   []steamMarketListing `json:"listings"`
	} `json:"data"`
}

type steamMarketListing struct {
	ListingID   string `json:"listingid"`
	Price       int64  `json:"unPrice"`
	Fee         int64  `json:"unFee"`
	Currency    int    `json:"eCurrency"`
	PriceText   string `json:"strSubtotal"`
	Description struct {
		Name              string             `json:"name"`
		MarketHashName    string             `json:"market_hash_name"`
		Descriptions      []steamDescription `json:"descriptions"`
		OwnerDescriptions []steamDescription `json:"owner_descriptions"`
	} `json:"description"`
}

type steamListingsQuery struct {
	AppID            int                 `json:"appid"`
	ItemName         string              `json:"strItemName"`
	Filters          map[string][]string `json:"filters"`
	AccessoryFilters map[string]string   `json:"accessoryFilters"`
	PropertyFilters  map[string]string   `json:"propertyFilters"`
	Start            int                 `json:"start"`
}

type steamDescription struct {
	Value string `json:"value"`
}

type cacheEntry struct {
	response priceResponse
	expires  time.Time
}

type rateLimitError struct {
	retryAfter time.Duration
	message    string
	source     string
}

func (e *rateLimitError) Error() string {
	return e.message
}

type marketClient struct {
	httpClient   *http.Client
	ratesClient  *http.Client
	marketURL    string
	pageURL      string
	ratesURL     string
	statePath    string
	mu           sync.Mutex
	cache        *cacheEntry
	rates        map[string]float64
	ratesExpires time.Time
	sessionReady bool
	retryUntil   time.Time
	forceAfter   time.Time
}

type savedMarketState struct {
	Version      int            `json:"version"`
	Response     *priceResponse `json:"response,omitempty"`
	CacheExpires time.Time      `json:"cache_expires,omitempty"`
}

const cacheVersion = 4

const (
	initialRetryDelay = time.Minute
	forceRetryDelay   = time.Minute
)

var gemPattern = regexp.MustCompile(`(?i)(?:prismatic\s+gem|gem)\s*[:：]\s*([^<\r\n]+)`)
var tagPattern = regexp.MustCompile(`<[^>]*>`)
var gemHTMLPattern = regexp.MustCompile(`(?is)<span\b[^>]*>\s*([^<>]+?)\s*</span>\s*<br\s*/?>\s*<span\b[^>]*>\s*(?:prismatic\s+gem|призматический\s+самоцвет)\s*</span>`)

func newMarketClient() *marketClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = false
	transport.DisableCompression = true
	ratesTransport := http.DefaultTransport.(*http.Transport).Clone()
	ratesTransport.ForceAttemptHTTP2 = true
	jar, _ := cookiejar.New(nil)
	return &marketClient{
		httpClient: &http.Client{Timeout: 15 * time.Second, Transport: transport, Jar: jar},
		ratesClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: ratesTransport,
		},
		marketURL: steamMarketURL,
		pageURL:   steamListingPageURL,
		ratesURL:  cbrRatesURL,
		statePath: "data/market-state.json",
	}
}

func (c *marketClient) loadState() error {
	if c.statePath == "" {
		return nil
	}
	data, err := os.ReadFile(c.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read market state: %w", err)
	}
	var state savedMarketState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode market state: %w", err)
	}
	if state.Response != nil && state.Version == cacheVersion {
		response := *state.Response
		repairedURLs := repairCachedListingURLs(&response)
		c.cache = &cacheEntry{response: response, expires: state.CacheExpires}
		if repairedURLs {
			log.Printf("updated cached Steam listing links to supported page offsets")
			c.persistState()
		}
	} else if state.Response != nil {
		log.Printf("discarding incompatible cached market response: version=%d expected=%d", state.Version, cacheVersion)
	}
	if c.cache == nil {
		log.Printf("no cached market response found; fetching listings on startup")
	} else {
		log.Printf("loaded cached market response: offers=%d updated_at=%s", c.cache.response.TotalCount, c.cache.response.UpdatedAt.Format(time.RFC3339))
	}
	return nil
}

func (c *marketClient) saveState() error {
	if c.statePath == "" {
		return nil
	}
	state := savedMarketState{Version: cacheVersion}
	if c.cache != nil {
		response := c.cache.response
		state.Response = &response
		state.CacheExpires = c.cache.expires
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode market state: %w", err)
	}
	dir := filepath.Dir(c.statePath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create market state directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".market-state-*")
	if err != nil {
		return fmt.Errorf("create temporary market state: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write market state: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close market state: %w", err)
	}
	if err := os.Rename(tempName, c.statePath); err != nil {
		return fmt.Errorf("replace market state: %w", err)
	}
	return nil
}

func (c *marketClient) persistState() {
	if err := c.saveState(); err != nil {
		log.Printf("persist market state: %v", err)
	}
}

func (c *marketClient) getListings(ctx context.Context, force bool) (priceResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if force && now.Before(c.forceAfter) {
		log.Printf("market refresh rejected by local manual-refresh limit: retry_in=%s", time.Until(c.forceAfter).Round(time.Second))
		return priceResponse{}, &rateLimitError{
			retryAfter: time.Until(c.forceAfter),
			message:    "Повторную проверку Steam можно запустить не чаще раза в минуту",
			source:     "app",
		}
	}
	if force {
		c.forceAfter = now.Add(forceRetryDelay)
	}
	if !force && c.cache != nil && now.Before(c.cache.expires) {
		return c.cache.response, nil
	}
	if now.Before(c.retryUntil) {
		log.Printf("market request deferred by local cooldown: retry_in=%s cached=%t", time.Until(c.retryUntil).Round(time.Second), c.cache != nil)
		if c.cache != nil {
			return c.staleResponse(time.Until(c.retryUntil)), nil
		}
		return priceResponse{}, &rateLimitError{
			retryAfter: time.Until(c.retryUntil),
			message:    "Приложение ожидает разрешённое время повторной проверки после предыдущего ответа Steam.",
			source:     "app",
		}
	}

	result, err := c.fetchListings(ctx)
	if err != nil {
		var rateLimit *rateLimitError
		retryAfter := initialRetryDelay
		if errors.As(err, &rateLimit) {
			retryAfter = rateLimit.retryAfter
			c.retryUntil = time.Now().Add(retryAfter)
		}
		log.Printf("Steam Market listings fetch failed: loaded=%d total=%d error=%v", len(result.Listings), result.TotalCount, err)
		if c.cache != nil {
			return c.staleResponse(retryAfter), nil
		}
		if len(result.Listings) > 0 {
			result.Warning = fmt.Sprintf("Загружено %d из %d лотов; следующая страница не получена: %v", len(result.Listings), result.TotalCount, err)
			result.RetryAfterSeconds = max(1, int(retryAfter.Round(time.Second).Seconds()))
			return result, nil
		}
		return priceResponse{}, err
	}

	log.Printf("Steam Market listings parsed: market_hash_name=%q loaded=%d total=%d complete=%t",
		itemHashName, len(result.Listings), result.TotalCount, result.Complete)
	c.cache = &cacheEntry{response: result, expires: time.Now().Add(cacheDuration)}
	c.retryUntil = time.Time{}
	c.persistState()
	return result, nil
}

func (c *marketClient) applyRateLimit(header string) time.Duration {
	if delay := parseRetryAfter(header, time.Now()); delay > 0 {
		c.retryUntil = time.Now().Add(delay)
		return delay
	}
	c.retryUntil = time.Now().Add(initialRetryDelay)
	return initialRetryDelay
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := time.ParseDuration(strings.TrimSpace(value) + "s"); err == nil && seconds > 0 {
		return seconds
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
}

func (c *marketClient) staleResponse(retryAfter time.Duration) priceResponse {
	result := c.cache.response
	result.Stale = true
	result.Warning = "Steam временно ограничил запросы. Показаны последние загруженные данные."
	result.RetryAfterSeconds = int(retryAfter.Round(time.Second).Seconds())
	if result.RetryAfterSeconds < 1 {
		result.RetryAfterSeconds = 1
	}
	return result
}

func (c *marketClient) fetchListings(ctx context.Context) (priceResponse, error) {
	result := priceResponse{
		Listings:            make([]listing, 0, 20),
		UpdatedAt:           time.Now().UTC(),
		AvailableCurrencies: []string{},
	}
	if !c.sessionReady {
		if err := c.openMarketSession(ctx); err != nil {
			return result, err
		}
		c.sessionReady = true
	}

	var exchangeRates map[string]float64
	var ratesLoaded, ratesAvailable bool
	var ratesWarning string
	for start := 0; start < maxMarketListings; {
		page, err := c.requestMarketPage(ctx, start)
		if err != nil {
			return result, err
		}
		if start == 0 {
			result.TotalCount = page.Data.TotalCount
		}
		if page.Data.Start != start {
			return result, fmt.Errorf("Steam Market returned page start=%d, expected %d", page.Data.Start, start)
		}
		if len(page.Data.Listings) == 0 {
			if page.Data.More {
				return result, fmt.Errorf("Steam Market returned an empty page at offset %d with more=true", start)
			}
			break
		}
		for _, item := range page.Data.Listings {
			if !strings.EqualFold(item.Description.MarketHashName, itemHashName) {
				continue
			}
			listingStart := start
			priceCents := item.Price + item.Fee
			if priceCents <= 0 {
				priceCents = parsePriceCents(item.PriceText)
			}
			if !ratesLoaded {
				ratesLoaded = true
				var err error
				exchangeRates, err = c.getExchangeRates(ctx)
				if err != nil {
					ratesWarning = "Не удалось получить курсы валют; доступна только исходная валюта Steam."
					log.Printf("load exchange rates: %v", err)
				} else {
					ratesAvailable = true
				}
			}
			priceValues := convertPriceValues(priceCents, item.Currency, exchangeRates)
			if len(priceValues) == 0 {
				return result, fmt.Errorf("unsupported Steam currency ID %d for listing %s", item.Currency, item.ListingID)
			}
			if len(result.Listings) == 0 {
				result.AvailableCurrencies = orderedAvailableCurrencies(priceValues)
			} else {
				result.AvailableCurrencies = intersectCurrencies(result.AvailableCurrencies, priceValues)
			}
			result.Listings = append(result.Listings, listing{
				ListingID:      item.ListingID,
				PageStart:      listingStart,
				Name:           item.Description.Name,
				MarketHashName: item.Description.MarketHashName,
				Gem:            findGem(item.Description.Descriptions, item.Description.OwnerDescriptions),
				PriceValues:    priceValues,
				Quantity:       1,
				URL:            listingURL(item.ListingID),
			})
		}
		start += len(page.Data.Listings)
		if !page.Data.More {
			result.Complete = start >= page.Data.TotalCount
			if !result.Complete {
				return result, fmt.Errorf("Steam Market ended pagination after %d of %d offers", start, page.Data.TotalCount)
			}
			break
		}
		if start >= page.Data.TotalCount {
			result.Complete = true
			break
		}
		if start >= maxMarketListings {
			return result, fmt.Errorf("stopped after the safety limit of %d market offers", maxMarketListings)
		}
	}
	if result.TotalCount == 0 {
		result.Complete = true
	}
	if ratesWarning != "" {
		result.Warning = ratesWarning
	}
	if ratesAvailable && len(result.AvailableCurrencies) == 0 && len(result.Listings) > 0 {
		result.Warning = "Нет общей валюты для отображения всех загруженных лотов."
	}
	return result, nil
}

func (c *marketClient) openMarketSession(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.pageURL, nil)
	if err != nil {
		return fmt.Errorf("create Steam listing-page request: %w", err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36")

	startedAt := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("open Steam listing page: %w", err)
	}
	defer resp.Body.Close()
	log.Printf("Steam listing page response: status=%d protocol=%s duration=%s retry_after=%q",
		resp.StatusCode, resp.Proto, time.Since(startedAt).Round(time.Millisecond), resp.Header.Get("Retry-After"))
	if resp.StatusCode == http.StatusTooManyRequests {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("Steam listing page 429 response body: %q", strings.TrimSpace(string(body)))
		delay := c.applyRateLimit(resp.Header.Get("Retry-After"))
		return &rateLimitError{
			retryAfter: delay,
			message:    "Steam Market вернул HTTP 429 при открытии страницы предмета.",
			source:     "steam",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Steam listing page returned HTTP %d", resp.StatusCode)
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<20)); err != nil {
		return fmt.Errorf("read Steam listing page: %w", err)
	}
	return nil
}

func (c *marketClient) requestMarketPage(ctx context.Context, start int) (steamMarketPage, error) {
	query := []steamListingsQuery{{
		AppID:            steamAppID,
		ItemName:         searchQuery,
		Filters:          map[string][]string{"Hero": {"npc_dota_hero_terrorblade"}, "Rarity": {"Rarity_Arcana"}},
		AccessoryFilters: map[string]string{},
		PropertyFilters:  map[string]string{},
		Start:            start,
	}}
	queryJSON, err := json.Marshal(query)
	if err != nil {
		return steamMarketPage{}, fmt.Errorf("encode Steam listings query: %w", err)
	}
	values := url.Values{
		"q":  {"QueryListingsForItem"},
		"qp": {string(queryJSON)},
	}
	requestURL := c.marketURL + "?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return steamMarketPage{}, fmt.Errorf("create Steam listings request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", c.pageURL)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36")

	startedAt := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return steamMarketPage{}, fmt.Errorf("request Steam Market page at offset %d: %w", start, err)
	}
	defer resp.Body.Close()
	log.Printf("Steam Market listings page: start=%d status=%d protocol=%s duration=%s retry_after=%q content_type=%q",
		start, resp.StatusCode, resp.Proto, time.Since(startedAt).Round(time.Millisecond), resp.Header.Get("Retry-After"), resp.Header.Get("Content-Type"))
	if resp.StatusCode == http.StatusTooManyRequests {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("Steam listings 429 response body: %q", strings.TrimSpace(string(body)))
		delay := c.applyRateLimit(resp.Header.Get("Retry-After"))
		c.sessionReady = false
		return steamMarketPage{}, &rateLimitError{
			retryAfter: delay,
			message:    fmt.Sprintf("Steam Market вернул HTTP 429 для страницы лотов %d.", start),
			source:     "steam",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return steamMarketPage{}, fmt.Errorf("Steam Market returned HTTP %d for page offset %d", resp.StatusCode, start)
	}
	var data steamMarketPage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&data); err != nil {
		return steamMarketPage{}, fmt.Errorf("decode Steam listings page at offset %d: %w", start, err)
	}
	return data, nil
}

func findGem(descriptionGroups ...[]steamDescription) string {
	for _, group := range descriptionGroups {
		for _, description := range group {
			if match := gemHTMLPattern.FindStringSubmatch(description.Value); len(match) > 1 {
				gem := strings.TrimSpace(html.UnescapeString(match[1]))
				if gem != "" {
					return gem
				}
			}
			text := strings.TrimSpace(html.UnescapeString(tagPattern.ReplaceAllString(description.Value, " ")))
			match := gemPattern.FindStringSubmatch(text)
			if len(match) > 1 {
				gem := strings.TrimSpace(match[1])
				gem = strings.Trim(gem, " .|")
				if gem != "" {
					return gem
				}
			}
		}
	}
	return "Не указано"
}

func parsePriceCents(value string) int64 {
	value = strings.TrimSpace(value)
	lastComma, lastDot := strings.LastIndex(value, ","), strings.LastIndex(value, ".")
	decimalAt := max(lastComma, lastDot)
	if (lastComma >= 0 && lastDot < 0 && digitsAfter(value, lastComma) == 3) ||
		(lastDot >= 0 && lastComma < 0 && digitsAfter(value, lastDot) == 3) {
		decimalAt = -1
	}
	var whole, fraction strings.Builder
	for index, char := range value {
		if char >= '0' && char <= '9' {
			if decimalAt >= 0 && index > decimalAt {
				fraction.WriteRune(char)
			} else {
				whole.WriteRune(char)
			}
		}
	}
	if whole.Len() == 0 {
		return 0
	}
	for fraction.Len() < 2 {
		fraction.WriteByte('0')
	}
	fractionText := fraction.String()
	if len(fractionText) > 2 {
		fractionText = fractionText[:2]
	}
	wholeValue, err := strconv.ParseInt(whole.String(), 10, 64)
	if err != nil {
		return 0
	}
	fractionValue, err := strconv.ParseInt(fractionText, 10, 64)
	if err != nil {
		return 0
	}
	return wholeValue*100 + fractionValue
}

func digitsAfter(value string, index int) int {
	digits := 0
	for _, char := range value[index+1:] {
		if char >= '0' && char <= '9' {
			digits++
		}
	}
	return digits
}

func main() {
	client := newMarketClient()
	if statePath := os.Getenv("STATE_FILE"); statePath != "" {
		client.statePath = statePath
	}
	if err := client.loadState(); err != nil {
		log.Fatalf("load market state: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/listings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		force := r.URL.Query().Get("force") == "1"
		data, err := client.getListings(r.Context(), force)
		if err != nil {
			var rateLimit *rateLimitError
			if errors.As(err, &rateLimit) {
				retryAfter := int(rateLimit.retryAfter.Round(time.Second).Seconds())
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				status := http.StatusTooManyRequests
				if rateLimit.source == "app" {
					status = http.StatusServiceUnavailable
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":               err.Error(),
					"retry_after_seconds": retryAfter,
					"source":              rateLimit.source,
				})
				return
			}
			log.Printf("load market listings: %v", err)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Не удалось загрузить данные Steam Market: " + err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(data); err != nil {
			log.Printf("encode listings response: %v", err)
		}
	})
	mux.Handle("/", frontendHandler("frontend/dist"))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	host := os.Getenv("HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	log.Printf("TB Market Parser listening on http://localhost:%s", port)
	if err := http.ListenAndServe(net.JoinHostPort(host, port), mux); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func frontendHandler(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(root, filepath.Clean(r.URL.Path))
		if r.URL.Path != "/" {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		index := filepath.Join(root, "index.html")
		if _, err := os.Stat(index); err == nil {
			http.ServeFile(w, r, index)
			return
		}
		http.Error(w, "Сначала соберите интерфейс: cd frontend && npm install && npm run build", http.StatusServiceUnavailable)
	})
}
