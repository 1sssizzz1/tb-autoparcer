package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
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

func TestNormalizePrismaticGemNameUsesSharedEnglishCategories(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Аленький цветочек", "Vermillion Renewal"},
		{"Глубокий зелёный", "Deep Green"},
		{"Глубокий синий", "Deep Blue"},
		{"Зеленая планета", "Earth Green"},
		{"Зеленая сосна", "Unhallowed Ground"},
		{"Золото Hand of Midas", "Gold"},
		{"Золотой", "Gold"},
		{"Красный", "Red"},
		{"Летнее тепло", "Summer Warmth"},
		{"Оранжевый", "Orange"},
		{"Отталкивающий чёрный", "Cursed Black"},
		{"Свет создателя", "Creator's Light"},
		{"Светло-зелёный", "Light Green"},
		{"Сдержанно-красный", "Rubiline"},
		{"Сине-зелёный", "Sea Green"},
		{"Синий", "Blue"},
		{"Синий кристалл", "Crystalline Blue"},
		{"Спокойный синий", "Placid Blue"},
		{"Тлеющее пламя", "Pyroclastic Flow"},
		{"Фиолетовый", "Purple"},
		{"Чемпионский зелёный", "Champion's Green"},
		{"Чемпионский синий", "Champion's Blue"},
		{"Чемпионский фиолетовый", "Champion's Purple"},
		{"Чумной серый", "Plague Grey"},
		{"Ярко-зелёный", "Bright Green"},
		{"Ярко-фиолетовый", "Bright Purple"},
		{" reflection's shade ", "Reflection's Shade"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizePrismaticGemName(tt.name); got != tt.want {
				t.Errorf("normalizePrismaticGemName(%q) = %q, want %q", tt.name, got, tt.want)
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

func TestFetchDotaListingsParsesTerrorbladeArcanaPagesAndGems(t *testing.T) {
	var pageRequests, gemRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/ajax/"):
			pageRequests.Add(1)
			if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
				t.Errorf("X-Requested-With = %q, want XMLHttpRequest", r.Header.Get("X-Requested-With"))
			}
			if !strings.Contains(r.URL.Path, "/Arcana/all/109/") || !strings.HasSuffix(r.URL.Path, "/all/109") {
				t.Errorf("unexpected market query path %q", r.URL.Path)
			}
			switch {
			case strings.Contains(r.URL.Path, "/1/56/"):
				_, _ = w.Write([]byte(`[[
					["342651391","3361758890","Fractal Horns of Inner Abysm",2950,"D2D2D2",false,"2 950<small></small>",[],"Fractal Horns of Inner Abysm"],
					["342651391","1485590338","Fractal Horns of Inner Abysm",6000,"D2D2D2",false,"6 000<small></small>",[],"Fractal Horns of Inner Abysm"],
					["999","999","Demon Eater",1000,"D2D2D2",false,"1 000<small></small>",[],"Demon Eater"]
				],2]`))
			case strings.Contains(r.URL.Path, "/2/56/"):
				_, _ = w.Write([]byte(`[[
					["342651391","1676325262","Fractal Horns of Inner Abysm",4100,"D2D2D2",false,"4 100<small></small>",[],"Fractal Horns of Inner Abysm"]
				],2]`))
			default:
				t.Errorf("unexpected Dota Market page path %q", r.URL.Path)
				http.NotFound(w, r)
			}
		case strings.HasPrefix(r.URL.Path, "/item/"):
			gemRequests.Add(1)
			switch {
			case strings.Contains(r.URL.Path, "3361758890"):
				_, _ = w.Write([]byte(`<p>Reflection&#39;s ShadeПризматический самоцвет</p>`))
			case strings.Contains(r.URL.Path, "1485590338"):
				_, _ = w.Write([]byte(`<p>Miasmatic GreyPrismatic Gem</p>`))
			case strings.Contains(r.URL.Path, "1676325262"):
				_, _ = w.Write([]byte(`<p>Tnim S&#39;nnamПризматический самоцвет</p>`))
			default:
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newMarketClient()
	client.dotaURL = server.URL
	client.ratesURL = server.URL + "/rates"
	listings, err := client.fetchDotaListings(t.Context())
	if err != nil {
		t.Fatalf("fetchDotaListings() error = %v", err)
	}
	if pageRequests.Load() != 2 || gemRequests.Load() != 3 {
		t.Fatalf("requests = pages:%d gems:%d, want pages:2 gems:3", pageRequests.Load(), gemRequests.Load())
	}
	if len(listings) != 3 {
		t.Fatalf("listings = %d, want 3", len(listings))
	}
	want := []struct {
		id, gem string
		price   int64
	}{
		{"342651391-3361758890", "Reflection's Shade", 295000},
		{"342651391-1485590338", "Miasmatic Grey", 600000},
		{"342651391-1676325262", "Tnim S'nnam", 410000},
	}
	for i, expected := range want {
		actual := listings[i]
		if actual.Source != "market.dota2.net" || actual.SourceItemID != expected.id ||
			actual.Gem != expected.gem || actual.PriceValues["RUB"] != expected.price {
			t.Errorf("listing[%d] = %+v, want source/item/gem/RUB = market.dota2.net/%s/%q/%d",
				i, actual, expected.id, expected.gem, expected.price)
		}
		if !strings.HasPrefix(actual.URL, server.URL+"/item/") {
			t.Errorf("listing[%d] URL = %q, want Dota Market item URL", i, actual.URL)
		}
	}
}

func TestExtractDotaGemWithAdditionalEmptySockets(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			name: "Russian prism gem followed by empty sockets",
			html: `<p >Tnim S&#39;nnamПризматический самоцветПустое гнездоОбщийПустое гнездоОбщий</p>`,
			want: "Tnim S'nnam",
		},
		{
			name: "English prism gem followed by socket description",
			html: `<p>Reflection&#39;s ShadePrismatic GemEmpty SocketCommon</p>`,
			want: "Reflection's Shade",
		},
		{
			name: "missing prism gem",
			html: `<p>Empty SocketCommon</p>`,
			want: "Не указано",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractDotaGem([]byte(tt.html)); got != tt.want {
				t.Errorf("extractDotaGem() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractDotaItemDetailsParsesMaximumAutoBuyPrice(t *testing.T) {
	body := []byte(`<div class="item-stat">
		<h3>Запросы на автопокупку</h3>
		<div class="rectanglestats">
			<div class="rectanglestat"><b>76</b><div>Всего запросов</div></div>
			<div class="rectanglestat"><b> 2 649.41&nbsp;<small></small> </b><div>1 запрос</div></div>
			<div class="rectanglestat"><b> 2 619.72&nbsp;<small></small> и менее</b><div>72 запроса</div></div>
			<div class="rectanglestat"><b> 2 700.00&nbsp;<small></small></b><div>2 запроса</div></div>
		</div>
	</div>
	<div class="item-stat"><h3>Статистика последних покупок</h3></div>
	<p>Tnim S&#39;nnamПризматический самоцветПустое гнездоОбщий</p>`)

	got := extractDotaItemDetails(body)
	if got.Gem != "Tnim S'nnam" {
		t.Errorf("Gem = %q, want Tnim S'nnam", got.Gem)
	}
	if got.AutoBuyPriceCents != 270000 {
		t.Errorf("AutoBuyPriceCents = %d, want maximum 270000", got.AutoBuyPriceCents)
	}
}

func TestExtractDotaItemDetailsWithoutAutoBuyRequests(t *testing.T) {
	got := extractDotaItemDetails([]byte(`<p>Reflection&#39;s ShadeПризматический самоцвет</p>`))
	if got.Gem != "Reflection's Shade" {
		t.Errorf("Gem = %q, want Reflection's Shade", got.Gem)
	}
	if got.AutoBuyPriceCents != 0 {
		t.Errorf("AutoBuyPriceCents = %d, want 0", got.AutoBuyPriceCents)
	}
}

func TestBuildDotaListingIncludesAutoBuyPriceValues(t *testing.T) {
	client := newMarketClient()
	client.dotaURL = "https://market.dota2.net"
	client.rates = map[string]float64{"USD": 90, "EUR": 100}
	client.ratesExpires = time.Now().Add(time.Hour)
	client.dotaItemDetailsCache = map[string]dotaItemDetails{
		"123-456": {Gem: "Reflection's Shade", AutoBuyPriceCents: 264941},
	}
	listings, err := client.buildDotaListings(t.Context(), []dotaMarketItem{{
		ClassID: "123", InstanceID: "456", Name: itemHashName, Price: 3000, Page: 1, Index: 0,
	}})
	if err != nil {
		t.Fatalf("buildDotaListings() error = %v", err)
	}
	if len(listings) != 1 {
		t.Fatalf("listings = %d, want 1", len(listings))
	}
	if got := listings[0].AutoBuyPriceValues["RUB"]; got != 264941 {
		t.Errorf("autobuy RUB = %d, want 264941", got)
	}
	if got := listings[0].AutoBuyPriceValues["USD"]; got != 2944 {
		t.Errorf("autobuy USD = %d, want 2944", got)
	}
}

func TestIncompleteSourceRefreshDoesNotReplaceCompleteCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/listing":
			http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "anonymous", Path: "/"})
			_, _ = w.Write([]byte("listing page"))
		case "/rates":
			_, _ = w.Write([]byte(`{"Valute":{"EUR":{"Nominal":1,"Value":100},"USD":{"Nominal":1,"Value":90}}}`))
		case "/market/actions":
			_, _ = w.Write([]byte(`{"data":{"more":false,"start":0,"total_count":1,"listings":[` +
				fixtureListing("1001", 2984, 447, "$34.31", "Reflection&#39;s Shade", "Призматический самоцвет") +
				`]}}`))
		default:
			http.Error(w, "Dota Market unavailable", http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()

	client := newMarketClient()
	client.marketURL = server.URL + "/market/actions"
	client.pageURL = server.URL + "/listing"
	client.dotaURL = server.URL + "/dota"
	client.ratesURL = server.URL + "/rates"
	client.statePath = ""
	client.cache = &cacheEntry{
		response: priceResponse{TotalCount: 99, Complete: true},
		expires:  time.Now().Add(-time.Second),
	}

	got, err := client.getListings(t.Context(), false)
	if err != nil {
		t.Fatalf("getListings() error = %v", err)
	}
	if got.Complete || len(got.Listings) != 1 || got.SteamTotalCount != 1 {
		t.Fatalf("unexpected partial response: %+v", got)
	}
	if client.cache.response.TotalCount != 99 {
		t.Fatalf("cached total_count = %d, want the previous complete value 99", client.cache.response.TotalCount)
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
	client.dotaURL = ""
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
	client.dotaURL = ""
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

func TestDotaMarketClientSupportsHTTP2(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("market.dota2.net request protocol = %s, want HTTP/2", r.Proto)
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/ajax/"):
			_, _ = w.Write([]byte(`[[],1]`))
		case strings.HasPrefix(r.URL.Path, "/item/"):
			_, _ = w.Write([]byte(`<div class="item-stat"><h3>Запросы на автопокупку</h3><div class="rectanglestat"><b>2 649.41</b><div>1 запрос</div></div></div><p>Reflection&#39;s ShadeПризматический самоцвет</p>`))
		default:
			http.NotFound(w, r)
		}
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	client := newMarketClient()
	client.dotaURL = server.URL
	client.dotaClient = server.Client()
	if _, _, err := client.requestDotaMarketPage(t.Context(), 1); err != nil {
		t.Fatalf("requestDotaMarketPage() error = %v", err)
	}
	details, err := client.requestDotaItemDetails(t.Context(), dotaMarketItem{
		ClassID: "123", InstanceID: "456", Name: itemHashName,
	})
	if err != nil {
		t.Fatalf("requestDotaItemDetails() error = %v", err)
	}
	if details.Gem != "Reflection's Shade" {
		t.Errorf("gem = %q, want Reflection's Shade", details.Gem)
	}
	if details.AutoBuyPriceCents != 264941 {
		t.Errorf("AutoBuyPriceCents = %d, want 264941", details.AutoBuyPriceCents)
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
	client.dotaURL = ""
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
	client.dotaURL = ""
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
	restarted.dotaURL = ""
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
	client.dotaURL = ""
	client.ratesURL = server.URL + "/rates"
	client.statePath = ""
	if _, err := client.getListings(t.Context(), false); err != nil {
		t.Fatalf("getListings() error = %v", err)
	}
}
