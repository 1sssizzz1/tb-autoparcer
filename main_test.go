package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestFindGem(t *testing.T) {
	tests := []struct {
		name         string
		descriptions []steamDescription
		want         string
	}{
		{
			name: "extracts gem from Steam HTML description",
			descriptions: []steamDescription{{
				Value: `<div><span style="color:red">Reflection&#39;s Shade</span><br><span>Призматический самоцвет</span></div>`,
			}},
			want: "Reflection's Shade",
		},
		{
			name: "extracts legacy colon format",
			descriptions: []steamDescription{{
				Value: "<div>Prismatic Gem: Creator&#39;s Light</div>",
			}},
			want: "Creator's Light",
		},
		{
			name: "checks owner descriptions",
			descriptions: []steamDescription{{
				Value: "Prismatic Gem: Rubiline",
			}},
			want: "Rubiline",
		},
		{
			name: "reports absent gem",
			want: "Не указано",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var groups [][]steamDescription
			if tt.name == "checks owner descriptions" {
				groups = append(groups, nil, tt.descriptions)
			} else {
				groups = append(groups, tt.descriptions)
			}
			if got := findGem(groups...); got != tt.want {
				t.Fatalf("findGem() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParsePriceCents(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  int64
	}{
		{"€36,36", 3636},
		{"$1,234.56", 123456},
		{"$1,234", 123400},
		{" 1.234,5 € ", 123450},
		{"not a price", 0},
	} {
		if got := parsePriceCents(tt.value); got != tt.want {
			t.Errorf("parsePriceCents(%q) = %d, want %d", tt.value, got, tt.want)
		}
	}
}

func TestFetchListingsLoadsPagesAndParsesIndividualOffers(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/listing":
			http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "anonymous", Path: "/"})
			_, _ = w.Write([]byte("listing page"))
		case "/rates":
			_, _ = w.Write([]byte(`{"Valute":{"EUR":{"Nominal":1,"Value":100},"USD":{"Nominal":1,"Value":90}}}`))
		case "/market/actions":
			if got := r.Header.Get("Accept-Encoding"); got != "" {
				t.Errorf("Steam Accept-Encoding = %q, want no compression", got)
			}
			requests++
			if r.Header.Get("Cookie") != "sessionid=anonymous" {
				t.Errorf("Cookie = %q, want anonymous session cookie", r.Header.Get("Cookie"))
			}
			if got := r.URL.Query().Get("q"); got != "QueryListingsForItem" {
				t.Errorf("q = %q, want QueryListingsForItem", got)
			}
			var queries []steamListingsQuery
			if err := json.Unmarshal([]byte(r.URL.Query().Get("qp")), &queries); err != nil {
				t.Fatalf("decode qp: %v", err)
			}
			if len(queries) != 1 || queries[0].AppID != steamAppID || queries[0].ItemName != itemHashName {
				t.Fatalf("unexpected query: %+v", queries)
			}
			if got := queries[0].Filters["Hero"]; len(got) != 1 || got[0] != "npc_dota_hero_terrorblade" {
				t.Errorf("Hero filter = %v", got)
			}
			if got := queries[0].Filters["Rarity"]; len(got) != 1 || got[0] != "Rarity_Arcana" {
				t.Errorf("Rarity filter = %v", got)
			}
			switch queries[0].Start {
			case 0:
				_, _ = w.Write([]byte(`{"data":{"more":true,"start":0,"total_count":3,"listings":[` +
					fixtureListing("1001", 2984, 447, "$34.31", "Reflection&#39;s Shade", "Призматический самоцвет") + `,` +
					fixtureListing("1002", 3020, 450, "$34.70", "Creator&#39;s Light", "Prismatic Gem") +
					`]}}`))
			case 2:
				_, _ = w.Write([]byte(`{"data":{"more":false,"start":2,"total_count":3,"listings":[` +
					fixtureListing("1003", 3162, 474, "€36,36", "Rubiline", "Призматический самоцвет") +
					`]}}`))
			default:
				t.Errorf("unexpected page start %d", queries[0].Start)
				http.Error(w, "unexpected offset", http.StatusBadRequest)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newMarketClient()
	client.marketURL = server.URL + "/market/actions"
	client.pageURL = server.URL + "/listing"
	client.ratesURL = server.URL + "/rates"
	client.statePath = ""
	got, err := client.getListings(t.Context(), false)
	if err != nil {
		t.Fatalf("getListings() error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("market page requests = %d, want 2", requests)
	}
	if !got.Complete || got.TotalCount != 3 || len(got.Listings) != 3 {
		t.Fatalf("unexpected response summary: complete=%t total=%d loaded=%d", got.Complete, got.TotalCount, len(got.Listings))
	}
	want := []struct {
		id, gem string
		prices  map[string]int64
	}{
		{"1001", "Reflection's Shade", map[string]int64{"RUB": 343100, "USD": 3812, "EUR": 3431}},
		{"1002", "Creator's Light", map[string]int64{"RUB": 347000, "USD": 3856, "EUR": 3470}},
		{"1003", "Rubiline", map[string]int64{"RUB": 363600, "USD": 4040, "EUR": 3636}},
	}
	for i, expected := range want {
		actual := got.Listings[i]
		if actual.ListingID != expected.id || actual.Gem != expected.gem ||
			actual.Quantity != 1 || actual.MarketHashName != itemHashName {
			t.Errorf("listing[%d] = %+v, want id=%s gem=%q", i, actual, expected.id, expected.gem)
		}
		for currency, price := range expected.prices {
			if actual.PriceValues[currency] != price {
				t.Errorf("listing[%d] price in %s = %d, want %d", i, currency, actual.PriceValues[currency], price)
			}
		}
		parsedURL, err := url.Parse(actual.URL)
		if err != nil {
			t.Fatalf("parse listing URL: %v", err)
		}
		if got := parsedURL.Query().Get("listingid"); got != actual.ListingID {
			t.Errorf("listing URL listingid = %q, want %q", got, actual.ListingID)
		}
		if got := parsedURL.Query().Get("detail"); got != actual.ListingID {
			t.Errorf("listing URL detail = %q, want %q", got, actual.ListingID)
		}
		if got := parsedURL.Query().Get("start"); got != "" {
			t.Errorf("listing URL unexpectedly contains start=%q", got)
		}
		if got.AvailableCurrencies == nil || len(got.AvailableCurrencies) != 3 {
			t.Fatalf("available currencies = %v, want RUB, USD, EUR", got.AvailableCurrencies)
		}
	}
}

func TestListingURLUsesListingDetailID(t *testing.T) {
	const wantURL = "https://steamcommunity.com/market/listings/570/Fractal%20Horns%20of%20Inner%20Abysm?category_Hero=npc_dota_hero_terrorblade&category_Rarity=Rarity_Arcana&listingid=4920724238053064405&detail=4920724238053064405"
	gotURL := listingURL("4920724238053064405")
	if gotURL != wantURL {
		t.Errorf("listingURL() = %q, want %q", gotURL, wantURL)
	}
	parsedURL, err := url.Parse(gotURL)
	if err != nil {
		t.Fatalf("parse listing URL: %v", err)
	}
	for _, key := range []string{"listingid", "detail"} {
		if got := parsedURL.Query().Get(key); got != "4920724238053064405" {
			t.Errorf("listing URL %s = %q, want listing ID", key, got)
		}
	}
	if got := parsedURL.Query().Get("start"); got != "" {
		t.Errorf("listing URL unexpectedly contains start=%q", got)
	}
}

func TestRepairCachedListingURLsAddsDetailID(t *testing.T) {
	response := priceResponse{Listings: []listing{
		{ListingID: "1001", PageStart: 1, URL: "https://steamcommunity.com/market/listings/570/item?start=1"},
		{ListingID: "1002", PageStart: 21, URL: "https://steamcommunity.com/market/listings/570/item?start=21"},
	}}
	if !repairCachedListingURLs(&response) {
		t.Fatal("repairCachedListingURLs() = false, want true")
	}
	for i, wantID := range []string{"1001", "1002"} {
		item := response.Listings[i]
		parsedURL, err := url.Parse(item.URL)
		if err != nil {
			t.Fatalf("parse listing[%d] URL: %v", i, err)
		}
		for _, key := range []string{"listingid", "detail"} {
			if got := parsedURL.Query().Get(key); got != wantID {
				t.Errorf("listing[%d] URL %s = %q, want %q", i, key, got, wantID)
			}
		}
	}
	if repairCachedListingURLs(&response) {
		t.Fatal("repairing cached URLs again unexpectedly changed them")
	}
}

func TestPaginationRateLimitReturnsExplicitPartialData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/listing":
			http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "anonymous", Path: "/"})
		case "/rates":
			_, _ = w.Write([]byte(`{"Valute":{"EUR":{"Nominal":1,"Value":100}}}`))
		case "/market/actions":
			var queries []steamListingsQuery
			if err := json.Unmarshal([]byte(r.URL.Query().Get("qp")), &queries); err != nil {
				t.Fatalf("decode qp: %v", err)
			}
			if queries[0].Start == 0 {
				_, _ = w.Write([]byte(`{"data":{"more":true,"start":0,"total_count":3,"listings":[` +
					fixtureListing("3001", 100, 15, "$1.15", "Reflection's Shade", "Prismatic Gem") + `]}}`))
				return
			}
			w.Header().Set("Retry-After", "30")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newMarketClient()
	client.marketURL = server.URL + "/market/actions"
	client.pageURL = server.URL + "/listing"
	client.ratesURL = server.URL + "/rates"
	client.statePath = ""
	got, err := client.getListings(t.Context(), false)
	if err != nil {
		t.Fatalf("getListings() error = %v, want partial response", err)
	}
	if got.Complete || got.TotalCount != 3 || len(got.Listings) != 1 {
		t.Fatalf("unexpected partial response: complete=%t total=%d loaded=%d", got.Complete, got.TotalCount, len(got.Listings))
	}
	if got.Warning == "" || got.RetryAfterSeconds != 30 {
		t.Fatalf("partial response warning=%q retry=%d, want warning and 30 seconds", got.Warning, got.RetryAfterSeconds)
	}
}

func fixtureListing(id string, price, fee int, subtotal, gem, label string) string {
	return fmt.Sprintf(`{"listingid":%q,"unPrice":%d,"unFee":%d,"eCurrency":3,"strSubtotal":%q,"description":{"name":%q,"market_hash_name":%q,"descriptions":[{"type":"html","value":"<div><span>%s</span><br><span>%s</span></div>"}]}}`,
		id, price, fee, subtotal, itemHashName, itemHashName, gem, label)
}

func TestExchangeRateClientSupportsHTTP2(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("rate request protocol = %s, want HTTP/2", r.Proto)
		}
		if r.Header.Get("Cookie") != "" {
			t.Errorf("rate request unexpectedly sent cookies: %q", r.Header.Get("Cookie"))
		}
		_, _ = w.Write([]byte(`{"Valute":{"EUR":{"Nominal":1,"Value":100}}}`))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	client := newMarketClient()
	client.ratesURL = server.URL
	client.ratesClient = server.Client()
	rates, err := client.getExchangeRates(t.Context())
	if err != nil {
		t.Fatalf("getExchangeRates() error = %v", err)
	}
	if rates["EUR"] != 100 {
		t.Fatalf("EUR rate = %v, want 100", rates["EUR"])
	}
}

func TestFetchListingsFallsBackToSteamCurrencyWhenRatesUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/listing":
			http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "steam-session", Path: "/"})
		case "/rates":
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		case "/market/actions":
			if r.Header.Get("Cookie") != "sessionid=steam-session" {
				t.Errorf("Steam cookie = %q, want Steam session cookie", r.Header.Get("Cookie"))
			}
			_, _ = w.Write([]byte(`{"data":{"more":false,"start":0,"total_count":1,"listings":[` +
				fixtureListing("4001", 2984, 447, "34,31 €", "Reflection's Shade", "Prismatic Gem") + `]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newMarketClient()
	client.marketURL = server.URL + "/market/actions"
	client.pageURL = server.URL + "/listing"
	client.ratesURL = server.URL + "/rates"
	client.statePath = ""
	client.ratesClient = server.Client()
	got, err := client.getListings(t.Context(), false)
	if err != nil {
		t.Fatalf("getListings() error = %v, want native-currency fallback", err)
	}
	if len(got.Listings) != 1 || got.Listings[0].PriceValues["EUR"] != 3431 {
		t.Fatalf("unexpected native-currency fallback: %+v", got)
	}
	if len(got.AvailableCurrencies) != 1 || got.AvailableCurrencies[0] != "EUR" {
		t.Fatalf("available currencies = %v, want EUR", got.AvailableCurrencies)
	}
	if got.Warning == "" {
		t.Fatal("expected a warning that exchange rates are unavailable")
	}
}

func TestConvertPriceToRUB(t *testing.T) {
	tests := []struct {
		name     string
		price    int64
		currency int
		rates    map[string]float64
		want     int64
		wantErr  bool
	}{
		{name: "converts euro using current rate", price: 3435, currency: 3, rates: map[string]float64{"EUR": 100}, want: 343500},
		{name: "converts a zero-decimal currency", price: 1000, currency: 8, rates: map[string]float64{"JPY": 0.6}, want: 60000},
		{name: "keeps rubles unchanged", price: 25025, currency: 5, want: 25025},
		{name: "rejects missing rate", price: 3435, currency: 3, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := convertPriceToRUB(tt.price, tt.currency, tt.rates)
			if (err != nil) != tt.wantErr {
				t.Fatalf("convertPriceToRUB() error = %v, wantErr %t", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("convertPriceToRUB() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFormatRUB(t *testing.T) {
	for _, tt := range []struct {
		kopecks int64
		want    string
	}{
		{0, "0,00 ₽"},
		{123456789, "1\u00a0234\u00a0567,89 ₽"},
	} {
		if got := formatRUB(tt.kopecks); got != tt.want {
			t.Errorf("formatRUB(%d) = %q, want %q", tt.kopecks, got, tt.want)
		}
	}
}

func TestRateLimitErrorIncludesReason(t *testing.T) {
	err := &rateLimitError{retryAfter: time.Minute, message: "ожидание"}
	if got := err.Error(); got != "ожидание" {
		t.Fatalf("Error() = %q, want %q", got, "ожидание")
	}
}

func TestForcedRefreshRespectsUpstreamBackoff(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()

	client := newMarketClient()
	client.marketURL = server.URL
	client.pageURL = server.URL
	client.statePath = ""
	client.retryUntil = time.Now().Add(10 * time.Minute)

	_, err := client.getListings(t.Context(), true)
	var rateLimit *rateLimitError
	if !errors.As(err, &rateLimit) || rateLimit.source != "app" {
		t.Fatalf("forced refresh error = %v, want local cooldown", err)
	}
	if requests != 0 {
		t.Fatalf("Steam requests = %d during cooldown, want 0", requests)
	}
}

func TestSavedCooldownDoesNotBlockStartupFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/listing":
			http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "anonymous", Path: "/"})
		case "/rates":
			_, _ = w.Write([]byte(`{"Valute":{"EUR":{"Nominal":1,"Value":100}}}`))
		case "/market/actions":
			_, _ = w.Write([]byte(`{"data":{"more":false,"start":0,"total_count":1,"listings":[` +
				fixtureListing("2001", 100, 15, "$1.15", "Reflection's Shade", "Prismatic Gem") + `]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	statePath := t.TempDir() + "/market-state.json"
	if err := os.WriteFile(statePath, []byte(`{"retry_until":"2099-01-01T00:00:00Z","retry_delay_seconds":3600}`), 0600); err != nil {
		t.Fatal(err)
	}

	restarted := newMarketClient()
	restarted.marketURL = server.URL + "/market/actions"
	restarted.pageURL = server.URL + "/listing"
	restarted.ratesURL = server.URL + "/rates"
	restarted.statePath = statePath
	if err := restarted.loadState(); err != nil {
		t.Fatalf("loadState() error = %v", err)
	}
	got, err := restarted.getListings(t.Context(), false)
	if err != nil {
		t.Fatalf("startup fetch error = %v", err)
	}
	if got.TotalCount != 1 || len(got.Listings) != 1 {
		t.Fatalf("startup offers = %d/%d, want 1/1", len(got.Listings), got.TotalCount)
	}
}

func TestMarketRequestDoesNotAdvertiseGzip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rates" {
			if got := r.Header.Get("Accept-Encoding"); got != "" {
				t.Errorf("Steam Accept-Encoding = %q, want no compression", got)
			}
		}
		switch r.URL.Path {
		case "/listing":
			http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "anonymous", Path: "/"})
		case "/rates":
			_, _ = w.Write([]byte(`{"Valute":{"EUR":{"Nominal":1,"Value":100}}}`))
		case "/market/actions":
			_, _ = w.Write([]byte(`{"data":{"more":false,"start":0,"total_count":0,"listings":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newMarketClient()
	client.marketURL = server.URL + "/market/actions"
	client.pageURL = server.URL + "/listing"
	client.ratesURL = server.URL + "/rates"
	client.statePath = ""
	if _, err := client.getListings(t.Context(), false); err != nil {
		t.Fatalf("getListings() error = %v", err)
	}
}
