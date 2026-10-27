package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	steamMarketURL = "https://steamcommunity.com/market/search/render/"
	steamAppID     = "570"
	searchQuery    = "Fractal Horns of Inner Abysm"
	itemHashName   = "Fractal Horns of Inner Abysm"
	cacheDuration  = 60 * time.Second
)

type listing struct {
	Name           string `json:"name"`
	MarketHashName string `json:"market_hash_name"`
	Gem            string `json:"gem"`
	PriceText      string `json:"price_text"`
	PriceCents     int64  `json:"price_cents"`
	Quantity       int    `json:"quantity"`
	URL            string `json:"url"`
}

type listingsResponse struct {
	Listings          []listing `json:"listings"`
	TotalCount        int       `json:"total_count"`
	UpdatedAt         time.Time `json:"updated_at"`
	Stale             bool      `json:"stale,omitempty"`
	Warning           string    `json:"warning,omitempty"`
	RetryAfterSeconds int       `json:"retry_after_seconds,omitempty"`
}

type steamSearchResponse struct {
	Success    bool `json:"success"`
	TotalCount int  `json:"total_count"`
	Results    []struct {
		Name          string `json:"name"`
		HashName      string `json:"hash_name"`
		SellListings  int    `json:"sell_listings"`
		SellPrice     int64  `json:"sell_price"`
		SellPriceText string `json:"sell_price_text"`
		AssetDetails  struct {
			Descriptions      []steamDescription `json:"descriptions"`
			OwnerDescriptions []steamDescription `json:"owner_descriptions"`
		} `json:"asset_description"`
	} `json:"results"`
}

type steamDescription struct {
	Value string `json:"value"`
}

type cacheEntry struct {
	response listingsResponse
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
	httpClient *http.Client
	marketURL  string
	statePath  string
	mu         sync.Mutex
	cache      *cacheEntry
	retryUntil time.Time
	forceAfter time.Time
}

type savedMarketState struct {
	Response     *listingsResponse `json:"response,omitempty"`
	CacheExpires time.Time         `json:"cache_expires,omitempty"`
}

const (
	initialRetryDelay = time.Minute
	forceRetryDelay   = time.Minute
)

var gemPattern = regexp.MustCompile(`(?i)(?:prismatic\s+gem|gem)\s*[:：]\s*([^<\r\n]+)`)
var tagPattern = regexp.MustCompile(`<[^>]*>`)

func newMarketClient() *marketClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = false
	transport.DisableCompression = true
	return &marketClient{
		httpClient: &http.Client{Timeout: 15 * time.Second, Transport: transport},
		marketURL:  steamMarketURL,
		statePath:  "data/market-state.json",
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
	if state.Response != nil {
		c.cache = &cacheEntry{response: *state.Response, expires: state.CacheExpires}
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
	state := savedMarketState{}
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

func (c *marketClient) getListings(ctx context.Context, force bool) (listingsResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if force && now.Before(c.forceAfter) {
		log.Printf("market refresh rejected by local manual-refresh limit: retry_in=%s", time.Until(c.forceAfter).Round(time.Second))
		return listingsResponse{}, &rateLimitError{
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
		return listingsResponse{}, &rateLimitError{
			retryAfter: time.Until(c.retryUntil),
			message:    "Приложение ожидает разрешённое время повторной проверки после предыдущего ответа Steam.",
			source:     "app",
		}
	}

	query := url.Values{
		"appid":               {steamAppID},
		"query":               {searchQuery},
		"start":               {"0"},
		"count":               {"100"},
		"search_descriptions": {"1"},
		"norender":            {"1"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.marketURL+"?"+query.Encode(), nil)
	if err != nil {
		return listingsResponse{}, fmt.Errorf("create Steam request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TBMarketParser/1.0 (local personal market viewer)")

	startedAt := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return listingsResponse{}, fmt.Errorf("request Steam Market: %w", err)
	}
	defer resp.Body.Close()
	log.Printf("Steam Market response: status=%d protocol=%s duration=%s retry_after=%q content_type=%q request_url=%q",
		resp.StatusCode, resp.Proto, time.Since(startedAt).Round(time.Millisecond), resp.Header.Get("Retry-After"), resp.Header.Get("Content-Type"), req.URL.String())
	if resp.StatusCode == http.StatusTooManyRequests {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if readErr != nil {
			log.Printf("read Steam 429 response body: %v", readErr)
		} else {
			log.Printf("Steam 429 response body: %q", strings.TrimSpace(string(body)))
		}
		retryAfter := c.applyRateLimit(resp.Header.Get("Retry-After"))
		c.persistState()
		if c.cache != nil {
			return c.staleResponse(retryAfter), nil
		}
		return listingsResponse{}, &rateLimitError{
			retryAfter: retryAfter,
			message:    "Steam Market вернул HTTP 429 и попросил подождать перед следующим запросом.",
			source:     "steam",
		}
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("Steam Market returned unexpected status: status=%d duration=%s",
			resp.StatusCode, time.Since(startedAt).Round(time.Millisecond))
		if c.cache != nil {
			return c.staleResponse(initialRetryDelay), nil
		}
		return listingsResponse{}, fmt.Errorf("Steam Market returned HTTP %d", resp.StatusCode)
	}

	var data steamSearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&data); err != nil {
		log.Printf("decode Steam Market response failed: %v", err)
		if c.cache != nil {
			return c.staleResponse(initialRetryDelay), nil
		}
		return listingsResponse{}, fmt.Errorf("decode Steam Market response: %w", err)
	}
	if !data.Success {
		log.Printf("Steam Market returned success=false: results=%d total=%d", len(data.Results), data.TotalCount)
		if c.cache != nil {
			return c.staleResponse(initialRetryDelay), nil
		}
		return listingsResponse{}, fmt.Errorf("Steam Market search response was unsuccessful")
	}

	result := parseListings(data)
	if len(result.Listings) == 0 {
		names := make([]string, 0, min(len(data.Results), 5))
		for _, item := range data.Results[:min(len(data.Results), 5)] {
			names = append(names, item.HashName)
		}
		log.Printf("Steam Market response contained no exact item match: query=%q results=%d total=%d sample_hash_names=%q",
			itemHashName, len(data.Results), data.TotalCount, names)
	} else {
		log.Printf("Steam Market listings parsed: market_hash_name=%q offers=%d price=%q",
			itemHashName, result.TotalCount, result.Listings[0].PriceText)
	}
	c.cache = &cacheEntry{response: result, expires: time.Now().Add(cacheDuration)}
	c.retryUntil = time.Time{}
	c.persistState()
	return result, nil
}

func (c *marketClient) applyRateLimit(header string) time.Duration {
	delay := initialRetryDelay
	if headerDelay := parseRetryAfter(header, time.Now()); headerDelay > delay {
		delay = headerDelay
	}
	c.retryUntil = time.Now().Add(delay)
	return delay
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

func (c *marketClient) staleResponse(retryAfter time.Duration) listingsResponse {
	result := c.cache.response
	result.Stale = true
	result.Warning = "Steam временно ограничил запросы. Показаны последние загруженные данные."
	result.RetryAfterSeconds = int(retryAfter.Round(time.Second).Seconds())
	if result.RetryAfterSeconds < 1 {
		result.RetryAfterSeconds = 1
	}
	return result
}

func parseListings(data steamSearchResponse) listingsResponse {
	result := listingsResponse{
		Listings:  make([]listing, 0, 1),
		UpdatedAt: time.Now().UTC(),
	}
	for _, item := range data.Results {
		if !strings.EqualFold(item.HashName, itemHashName) {
			continue
		}
		gem := findGem(item.AssetDetails.Descriptions, item.AssetDetails.OwnerDescriptions)
		result.TotalCount += item.SellListings
		result.Listings = append(result.Listings, listing{
			Name:           item.Name,
			MarketHashName: item.HashName,
			Gem:            gem,
			PriceText:      item.SellPriceText,
			PriceCents:     item.SellPrice,
			Quantity:       item.SellListings,
			URL: "https://steamcommunity.com/market/listings/" + steamAppID + "/" + url.PathEscape(item.HashName) +
				"?category_Hero=npc_dota_hero_terrorblade&category_Rarity=Rarity_Arcana",
		})
	}
	return result
}

func findGem(descriptionGroups ...[]steamDescription) string {
	for _, group := range descriptionGroups {
		for _, description := range group {
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
