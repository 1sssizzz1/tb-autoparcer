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
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

type steamMarketPage struct {
	Data struct {
		More       bool                 `json:"more"`
		Start      int                  `json:"start"`
		TotalCount int                  `json:"total_count"`
		Listings   []steamMarketListing `json:"listings"`
	} `json:"data"`
}

type dotaMarketItem struct {
	ClassID    string
	InstanceID string
	Name       string
	Price      float64
	Page       int
	Index      int
}

type dotaItemDetails struct {
	Gem               string
	AutoBuyPriceCents int64
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

var gemPattern = regexp.MustCompile(`(?i)(?:prismatic\s+gem|gem)\s*[:：]\s*([^<\r\n]+)`)

var tagPattern = regexp.MustCompile(`<[^>]*>`)

var gemHTMLPattern = regexp.MustCompile(`(?is)<span\b[^>]*>\s*([^<>]+?)\s*</span>\s*<br\s*/?>\s*<span\b[^>]*>\s*(?:prismatic\s+gem|призматический\s+самоцвет)\s*</span>`)

var dotaGemHTMLPattern = regexp.MustCompile(`(?is)<p\b[^>]*>\s*([^<>]+?)\s*(?:призматический\s+самоцвет|prismatic\s+gem)`)

var dotaAutoBuySectionPattern = regexp.MustCompile(`(?is)<h3\b[^>]*>\s*Запросы на автопокупку\s*</h3>(.*)`)
var dotaSectionHeadingPattern = regexp.MustCompile(`(?is)<h3\b`)
var dotaAutoBuyTierPattern = regexp.MustCompile(`(?is)<div\b[^>]*class="rectanglestat"[^>]*>\s*<b\b[^>]*>(.*?)</b>\s*<div\b[^>]*>(.*?)</div>`)

var prismaticGemNames = map[string]string{
	"аленький цветочек":      "Vermillion Renewal",
	"глубокий зелёный":       "Deep Green",
	"глубокий зеленый":       "Deep Green",
	"глубокий синий":         "Deep Blue",
	"зелёная планета":        "Earth Green",
	"зеленая планета":        "Earth Green",
	"зелёная сосна":          "Unhallowed Ground",
	"зеленая сосна":          "Unhallowed Ground",
	"золото hand of midas":   "Gold",
	"золотой":                "Gold",
	"красный":                "Red",
	"летнее тепло":           "Summer Warmth",
	"оранжевый":              "Orange",
	"отталкивающий чёрный":   "Cursed Black",
	"отталкивающий черный":   "Cursed Black",
	"свет создателя":         "Creator's Light",
	"светло-зелёный":         "Light Green",
	"светло-зеленый":         "Light Green",
	"сдержанно-красный":      "Rubiline",
	"сине-зелёный":           "Sea Green",
	"сине-зеленый":           "Sea Green",
	"синий":                  "Blue",
	"синий кристалл":         "Crystalline Blue",
	"спокойный синий":        "Placid Blue",
	"тлеющее пламя":          "Pyroclastic Flow",
	"фиолетовый":             "Purple",
	"чемпионский зелёный":    "Champion's Green",
	"чемпионский зеленый":    "Champion's Green",
	"чемпионский синий":      "Champion's Blue",
	"чемпионский фиолетовый": "Champion's Purple",
	"чумной серый":           "Plague Grey",
	"ярко-зелёный":           "Bright Green",
	"ярко-зеленый":           "Bright Green",
	"ярко-фиолетовый":        "Bright Purple",
}

var canonicalPrismaticGemNames = map[string]string{
	"blue":                   "Blue",
	"bright green":           "Bright Green",
	"bright purple":          "Bright Purple",
	"brusque britches beige": "Brusque Britches Beige",
	"champion's blue":        "Champion's Blue",
	"champion's green":       "Champion's Green",
	"champion's purple":      "Champion's Purple",
	"creator's light":        "Creator's Light",
	"crystalline blue":       "Crystalline Blue",
	"cursed black":           "Cursed Black",
	"deep blue":              "Deep Blue",
	"deep green":             "Deep Green",
	"diretide orange":        "Diretide Orange",
	"dredge earth":           "Dredge Earth",
	"dungeon doom":           "Dungeon Doom",
	"earth green":            "Earth Green",
	"explosive burst":        "Explosive Burst",
	"glacial flow":           "Glacial Flow",
	"gold":                   "Gold",
	"light green":            "Light Green",
	"miasmatic grey":         "Miasmatic Grey",
	"orange":                 "Orange",
	"placid blue":            "Placid Blue",
	"plague grey":            "Plague Grey",
	"plushy shag":            "Plushy Shag",
	"purple":                 "Purple",
	"pyroclastic flow":       "Pyroclastic Flow",
	"reflection's shade":     "Reflection's Shade",
	"rubiline":               "Rubiline",
	"sea green":              "Sea Green",
	"ships in the night":     "Ships in the Night",
	"summer warmth":          "Summer Warmth",
	"tnim s'nnam":            "Tnim S'nnam",
	"unhallowed ground":      "Unhallowed Ground",
	"vermillion renewal":     "Vermillion Renewal",
}

func normalizePrismaticGemName(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if canonical, ok := prismaticGemNames[normalized]; ok {
		return canonical
	}
	if canonical, ok := canonicalPrismaticGemNames[normalized]; ok {
		return canonical
	}
	return strings.TrimSpace(name)
}

func (c *marketClient) fetchSteamListings(ctx context.Context) (priceResponse, error) {
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
			result.SteamTotalCount = page.Data.TotalCount
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
				Source:         "Steam Market",
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

func (c *marketClient) fetchDotaListings(ctx context.Context) ([]listing, error) {
	firstPage, pages, err := c.requestDotaMarketPage(ctx, 1)
	if err != nil {
		return nil, err
	}
	if pages < 1 {
		return nil, fmt.Errorf("market.dota2.net returned invalid page count %d", pages)
	}
	if pages > maxDotaMarketPages {
		return nil, fmt.Errorf("market.dota2.net returned %d pages, safety limit is %d", pages, maxDotaMarketPages)
	}

	items := make([]dotaMarketItem, 0, pages*dotaMarketPageSize)
	if err := parseDotaPageItems(firstPage.Rows, 1, &items); err != nil {
		return nil, err
	}
	for page := 2; page <= pages; page++ {
		result, gotPages, err := c.requestDotaMarketPage(ctx, page)
		if err != nil {
			listings, gemErr := c.buildDotaListings(ctx, items)
			return listings, errors.Join(fmt.Errorf("load market.dota2.net page %d: %w", page, err), gemErr)
		}
		if gotPages != pages {
			listings, gemErr := c.buildDotaListings(ctx, items)
			return listings, errors.Join(fmt.Errorf("market.dota2.net page count changed from %d to %d", pages, gotPages), gemErr)
		}
		if err := parseDotaPageItems(result.Rows, page, &items); err != nil {
			listings, gemErr := c.buildDotaListings(ctx, items)
			return listings, errors.Join(err, gemErr)
		}
	}
	return c.buildDotaListings(ctx, items)
}

func (c *marketClient) requestDotaMarketPage(ctx context.Context, page int) (dotaMarketPage, int, error) {
	requestURL := fmt.Sprintf("%s/ajax/i_popularity/Arcana/all/109/%d/%d/0;500000/all/109?sd=desc",
		strings.TrimRight(c.dotaURL, "/"), page, dotaMarketPageSize)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return dotaMarketPage{}, 0, fmt.Errorf("create market.dota2.net page request: %w", err)
	}
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Referer", strings.TrimRight(c.dotaURL, "/")+"/?r=Arcana&t=109&sd=desc")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36")

	resp, err := c.dotaClient.Do(req)
	if err != nil {
		return dotaMarketPage{}, 0, fmt.Errorf("request market.dota2.net page %d: %w", page, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
		return dotaMarketPage{}, 0, fmt.Errorf("market.dota2.net returned HTTP %d for page %d", resp.StatusCode, page)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return dotaMarketPage{}, 0, fmt.Errorf("read market.dota2.net page %d: %w", page, err)
	}
	var response []json.RawMessage
	if err := json.Unmarshal(body, &response); err != nil {
		return dotaMarketPage{}, 0, fmt.Errorf("decode market.dota2.net page %d: %w", page, err)
	}
	if len(response) != 2 {
		return dotaMarketPage{}, 0, fmt.Errorf("market.dota2.net page %d returned %d response fields, want 2", page, len(response))
	}
	var result dotaMarketPage
	if err := json.Unmarshal(response[0], &result.Rows); err != nil {
		return dotaMarketPage{}, 0, fmt.Errorf("decode market.dota2.net items on page %d: %w", page, err)
	}
	var pages int
	if err := json.Unmarshal(response[1], &pages); err != nil {
		return dotaMarketPage{}, 0, fmt.Errorf("decode market.dota2.net page count: %w", err)
	}
	return result, pages, nil
}

func parseDotaPageItems(rows [][]json.RawMessage, page int, items *[]dotaMarketItem) error {
	for index, row := range rows {
		if len(row) < 4 {
			return fmt.Errorf("market.dota2.net row %d on page %d has %d fields, want at least 4", index, page, len(row))
		}
		var item dotaMarketItem
		for fieldIndex, target := range []*string{&item.ClassID, &item.InstanceID, &item.Name} {
			if err := json.Unmarshal(row[fieldIndex], target); err != nil {
				return fmt.Errorf("decode market.dota2.net row %d field %d: %w", index, fieldIndex, err)
			}
		}
		if err := json.Unmarshal(row[3], &item.Price); err != nil {
			return fmt.Errorf("decode market.dota2.net row %d price: %w", index, err)
		}
		if !strings.Contains(strings.ToLower(item.Name), strings.ToLower(itemHashName)) {
			continue
		}
		if item.ClassID == "" || item.InstanceID == "" || item.Price <= 0 {
			return fmt.Errorf("market.dota2.net row %d has invalid listing identity or price", index)
		}
		item.Page = page
		item.Index = index
		*items = append(*items, item)
	}
	return nil
}

func dotaItemURL(baseURL, classID, instanceID, name string) string {
	return strings.TrimRight(baseURL, "/") + "/item/" +
		url.PathEscape(classID+"-"+instanceID+"-"+name) + "/"
}

func dotaGemCacheKey(item listing) string {
	return item.SourceItemID
}

func (c *marketClient) buildDotaListings(ctx context.Context, items []dotaMarketItem) ([]listing, error) {
	details, lookupErrors := c.resolveDotaItemDetails(ctx, items)
	if lookupErrors > 0 {
		log.Printf("market.dota2.net gem details unavailable: items=%d failures=%d", len(items), lookupErrors)
	}
	rates, ratesErr := c.getExchangeRates(ctx)
	if ratesErr != nil {
		log.Printf("load exchange rates for market.dota2.net prices: %v", ratesErr)
	}
	listings := make([]listing, 0, len(items))
	for _, item := range items {
		priceCents := int64(math.Round(item.Price * 100))
		priceValues := convertPriceValues(priceCents, 5, rates)
		key := item.ClassID + "-" + item.InstanceID
		itemDetails := details[key]
		gem := itemDetails.Gem
		if gem == "" {
			gem = "Не указано"
		}
		autoBuyPriceValues := convertPriceValues(itemDetails.AutoBuyPriceCents, 5, rates)
		if itemDetails.AutoBuyPriceCents <= 0 {
			autoBuyPriceValues = nil
		}
		listings = append(listings, listing{
			ListingID:          fmt.Sprintf("dota2net-%s-%s-%d-%d", item.ClassID, item.InstanceID, item.Page, item.Index),
			Name:               item.Name,
			MarketHashName:     item.Name,
			Source:             "market.dota2.net",
			SourceItemID:       key,
			Gem:                gem,
			PriceValues:        priceValues,
			AutoBuyPriceValues: autoBuyPriceValues,
			Quantity:           1,
			URL:                dotaItemURL(c.dotaURL, item.ClassID, item.InstanceID, item.Name),
		})
	}
	if lookupErrors > 0 {
		return listings, fmt.Errorf("could not load gem details for %d of %d market.dota2.net offers", lookupErrors, len(items))
	}
	return listings, nil
}

func (c *marketClient) resolveDotaItemDetails(ctx context.Context, items []dotaMarketItem) (map[string]dotaItemDetails, int) {
	if c.dotaItemDetailsCache == nil {
		c.dotaItemDetailsCache = make(map[string]dotaItemDetails)
	}
	lookups := make(map[string]dotaMarketItem)
	for _, item := range items {
		key := item.ClassID + "-" + item.InstanceID
		if _, cached := c.dotaItemDetailsCache[key]; !cached {
			lookups[key] = item
		}
	}
	if len(lookups) == 0 {
		return c.dotaItemDetailsCache, 0
	}
	type gemResult struct {
		key     string
		details dotaItemDetails
		err     error
	}
	jobs := make(chan struct {
		key  string
		item dotaMarketItem
	}, len(lookups))
	results := make(chan gemResult, len(lookups))
	workerCount := min(dotaGemWorkers, len(lookups))
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				details, err := c.requestDotaItemDetails(ctx, job.item)
				results <- gemResult{key: job.key, details: details, err: err}
			}
		}()
	}
	for key, item := range lookups {
		jobs <- struct {
			key  string
			item dotaMarketItem
		}{key: key, item: item}
	}
	close(jobs)
	workers.Wait()
	close(results)
	failures := 0
	for result := range results {
		if result.err != nil {
			failures++
			continue
		}
		c.dotaItemDetailsCache[result.key] = result.details
	}
	return c.dotaItemDetailsCache, failures
}

func (c *marketClient) requestDotaGem(ctx context.Context, item dotaMarketItem) (string, error) {
	details, err := c.requestDotaItemDetails(ctx, item)
	if err != nil {
		return "", err
	}
	return details.Gem, nil
}

func (c *marketClient) requestDotaItemDetails(ctx context.Context, item dotaMarketItem) (dotaItemDetails, error) {
	requestURL := dotaItemURL(c.dotaURL, item.ClassID, item.InstanceID, item.Name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return dotaItemDetails{}, fmt.Errorf("create market.dota2.net item request: %w", err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Referer", strings.TrimRight(c.dotaURL, "/")+"/?r=Arcana&t=109&sd=desc")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36")
	resp, err := c.dotaClient.Do(req)
	if err != nil {
		return dotaItemDetails{}, fmt.Errorf("request market.dota2.net item details: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
		return dotaItemDetails{}, fmt.Errorf("market.dota2.net item details returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return dotaItemDetails{}, fmt.Errorf("read market.dota2.net item details: %w", err)
	}
	return extractDotaItemDetails(body), nil
}

func extractDotaGem(body []byte) string {
	match := dotaGemHTMLPattern.FindSubmatch(body)
	if len(match) < 2 {
		return "Не указано"
	}
	gem := normalizePrismaticGemName(html.UnescapeString(string(match[1])))
	if gem == "" {
		return "Не указано"
	}
	return gem
}

func extractDotaItemDetails(body []byte) dotaItemDetails {
	details := dotaItemDetails{Gem: extractDotaGem(body)}
	section := dotaAutoBuySectionPattern.FindSubmatch(body)
	if len(section) < 2 {
		return details
	}
	content := section[1]
	if nextHeading := dotaSectionHeadingPattern.FindIndex(content); nextHeading != nil {
		content = content[:nextHeading[0]]
	}
	for _, tier := range dotaAutoBuyTierPattern.FindAllSubmatch(content, -1) {
		if len(tier) < 3 {
			continue
		}
		label := strings.TrimSpace(html.UnescapeString(tagPattern.ReplaceAllString(string(tier[2]), " ")))
		if strings.Contains(strings.ToLower(label), "всего запросов") {
			continue
		}
		priceText := strings.TrimSpace(html.UnescapeString(tagPattern.ReplaceAllString(string(tier[1]), " ")))
		price := parsePriceCents(priceText)
		if price > details.AutoBuyPriceCents {
			details.AutoBuyPriceCents = price
		}
	}
	return details
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
				gem := normalizePrismaticGemName(html.UnescapeString(match[1]))
				if gem != "" {
					return gem
				}
			}
			text := strings.TrimSpace(html.UnescapeString(tagPattern.ReplaceAllString(description.Value, " ")))
			match := gemPattern.FindStringSubmatch(text)
			if len(match) > 1 {
				gem := normalizePrismaticGemName(match[1])
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
