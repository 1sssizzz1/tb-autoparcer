package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	steamMarketURL      = "https://steamcommunity.com/market/actions"
	steamListingPageURL = "https://steamcommunity.com/market/listings/570/Fractal%20Horns%20of%20Inner%20Abysm?category_Hero=npc_dota_hero_terrorblade&category_Rarity=Rarity_Arcana"
	dotaMarketURL       = "https://market.dota2.net"
	cbrRatesURL         = "https://www.cbr-xml-daily.ru/daily_json.js"
	steamAppID          = 570
	searchQuery         = "Fractal Horns of Inner Abysm"
	itemHashName        = "Fractal Horns of Inner Abysm"
	cacheDuration       = 5 * time.Minute
	ratesCacheDuration  = 12 * time.Hour
	maxMarketListings   = 2000
	dotaMarketPageSize  = 56
	maxDotaMarketPages  = 20
	dotaGemWorkers      = 4
)

type listing struct {
	ListingID          string           `json:"listing_id"`
	PageStart          int              `json:"page_start"`
	Name               string           `json:"name"`
	MarketHashName     string           `json:"market_hash_name"`
	Source             string           `json:"market_source"`
	SourceItemID       string           `json:"source_item_id,omitempty"`
	Gem                string           `json:"gem"`
	PriceValues        map[string]int64 `json:"price_values"`
	AutoBuyPriceValues map[string]int64 `json:"autobuy_price_values,omitempty"`
	Quantity           int              `json:"quantity"`
	URL                string           `json:"url"`
}

type priceResponse struct {
	Listings            []listing `json:"listings"`
	TotalCount          int       `json:"total_count"`
	SteamTotalCount     int       `json:"steam_total_count"`
	Dota2NetTotalCount  int       `json:"dota2net_total_count"`
	UpdatedAt           time.Time `json:"updated_at"`
	Complete            bool      `json:"complete"`
	AvailableCurrencies []string  `json:"available_currencies"`
	Stale               bool      `json:"stale,omitempty"`
	Warning             string    `json:"warning,omitempty"`
	RetryAfterSeconds   int       `json:"retry_after_seconds,omitempty"`
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
		if item.Source != "" && item.Source != "Steam Market" {
			continue
		}
		if item.URL != listingURL(item.ListingID) {
			item.URL = listingURL(item.ListingID)
			changed = true
		}
	}
	return changed
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
	httpClient           *http.Client
	dotaClient           *http.Client
	ratesClient          *http.Client
	marketURL            string
	pageURL              string
	dotaURL              string
	ratesURL             string
	statePath            string
	mu                   sync.Mutex
	cache                *cacheEntry
	rates                map[string]float64
	ratesExpires         time.Time
	sessionReady         bool
	retryUntil           time.Time
	forceAfter           time.Time
	dotaItemDetailsCache map[string]dotaItemDetails
}

type savedMarketState struct {
	Version      int            `json:"version"`
	Response     *priceResponse `json:"response,omitempty"`
	CacheExpires time.Time      `json:"cache_expires,omitempty"`
}

const cacheVersion = 9

const (
	initialRetryDelay = time.Minute
	forceRetryDelay   = time.Minute
)

func newMarketClient() *marketClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = false
	transport.DisableCompression = true
	dotaTransport := http.DefaultTransport.(*http.Transport).Clone()
	dotaTransport.ForceAttemptHTTP2 = true
	ratesTransport := http.DefaultTransport.(*http.Transport).Clone()
	ratesTransport.ForceAttemptHTTP2 = true
	jar, _ := cookiejar.New(nil)
	return &marketClient{
		httpClient: &http.Client{Timeout: 15 * time.Second, Transport: transport, Jar: jar},
		dotaClient: &http.Client{Timeout: 15 * time.Second, Transport: dotaTransport},
		ratesClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: ratesTransport,
		},
		marketURL:            steamMarketURL,
		pageURL:              steamListingPageURL,
		dotaURL:              dotaMarketURL,
		ratesURL:             cbrRatesURL,
		statePath:            "data/market-state.json",
		dotaItemDetailsCache: make(map[string]dotaItemDetails),
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
		for _, item := range response.Listings {
			if item.Source == "market.dota2.net" && item.Gem != "" {
				details := dotaItemDetails{Gem: item.Gem}
				details.AutoBuyPriceCents = item.AutoBuyPriceValues["RUB"]
				c.dotaItemDetailsCache[dotaGemCacheKey(item)] = details
			}
		}
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
	if !result.Complete {
		log.Printf("market sources returned partial data; leaving the last complete cache unchanged")
		return result, nil
	}
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
	result, steamErr := c.fetchSteamListings(ctx)
	steamComplete := steamErr == nil && result.Complete
	if steamErr != nil {
		log.Printf("Steam Market source fetch failed: %v", steamErr)
		appendMarketWarning(&result, "Не удалось обновить данные Steam Market: "+steamErr.Error())
		if !c.retryUntil.IsZero() {
			result.RetryAfterSeconds = max(1, int(time.Until(c.retryUntil).Round(time.Second).Seconds()))
		}
	}

	if c.dotaURL == "" {
		if steamErr != nil && len(result.Listings) == 0 {
			return result, steamErr
		}
		return result, nil
	}

	dotaListings, dotaErr := c.fetchDotaListings(ctx)
	if dotaErr != nil {
		log.Printf("market.dota2.net source fetch failed: %v", dotaErr)
		appendMarketWarning(&result, "Не удалось обновить market.dota2.net: "+dotaErr.Error())
	}
	for _, item := range dotaListings {
		if len(result.Listings) == 0 {
			result.AvailableCurrencies = orderedAvailableCurrencies(item.PriceValues)
		} else {
			result.AvailableCurrencies = intersectCurrencies(result.AvailableCurrencies, item.PriceValues)
		}
		result.Listings = append(result.Listings, item)
	}
	result.Dota2NetTotalCount = len(dotaListings)

	result.TotalCount = result.SteamTotalCount + result.Dota2NetTotalCount
	result.Complete = steamComplete && dotaErr == nil
	if steamErr != nil && dotaErr != nil && len(result.Listings) == 0 {
		return result, fmt.Errorf("fetch market sources: Steam Market: %v; market.dota2.net: %w", steamErr, dotaErr)
	}
	return result, nil
}

func appendMarketWarning(response *priceResponse, warning string) {
	if response.Warning != "" {
		response.Warning += " "
	}
	response.Warning += warning
}

type dotaMarketPage struct {
	Rows [][]json.RawMessage
}
