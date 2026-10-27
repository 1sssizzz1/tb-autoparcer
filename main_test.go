package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
			name: "extracts gem from HTML description",
			descriptions: []steamDescription{
				{Value: "<div>Prismatic Gem: Creator&#39;s Light</div>"},
			},
			want: "Creator's Light",
		},
		{
			name: "checks owner descriptions",
			descriptions: []steamDescription{
				{Value: "Prismatic Gem: Rubiline"},
			},
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

func TestParseListingsKeepsOnlyTerrorbladeArcana(t *testing.T) {
	const response = `{
		"success": true,
		"total_count": 2,
		"results": [
			{"name":"Fractal Horns of Inner Abysm - Horns","hash_name":"Fractal Horns of Inner Abysm - Horns","sell_listings":288},
			{"name":"Fractal Horns of Inner Abysm","hash_name":"Fractal Horns of Inner Abysm","sell_listings":272,"sell_price":3822,"sell_price_text":"$38.22"}
		]
	}`

	var data steamSearchResponse
	if err := json.NewDecoder(strings.NewReader(response)).Decode(&data); err != nil {
		t.Fatal(err)
	}

	got := parseListings(data)
	if len(got.Listings) != 1 {
		t.Fatalf("got %d listings, want 1", len(got.Listings))
	}
	item := got.Listings[0]
	if item.MarketHashName != itemHashName || item.Quantity != 272 || item.PriceCents != 3822 {
		t.Fatalf("unexpected Arcana listing: %+v", item)
	}
	const wantURL = "https://steamcommunity.com/market/listings/570/Fractal%20Horns%20of%20Inner%20Abysm?category_Hero=npc_dota_hero_terrorblade&category_Rarity=Rarity_Arcana"
	if item.URL != wantURL {
		t.Fatalf("listing URL = %q, want %q", item.URL, wantURL)
	}
	if got.TotalCount != 272 {
		t.Fatalf("total_count = %d, want active listing count 272", got.TotalCount)
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
		if got := r.URL.Query().Get("query"); got != searchQuery {
			t.Errorf("query = %q, want %q", got, searchQuery)
		}
		_, _ = w.Write([]byte(`{"success":true,"results":[{"name":"Fractal Horns of Inner Abysm","hash_name":"Fractal Horns of Inner Abysm","sell_listings":1,"sell_price":100,"sell_price_text":"$1.00"}]}`))
	}))
	defer server.Close()

	client := newMarketClient()
	client.marketURL = server.URL
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"results":[{"name":"Fractal Horns of Inner Abysm","hash_name":"Fractal Horns of Inner Abysm","sell_listings":7,"sell_price":100,"sell_price_text":"$1.00"}]}`))
	}))

	statePath := t.TempDir() + "/market-state.json"
	if err := os.WriteFile(statePath, []byte(`{"retry_until":"2099-01-01T00:00:00Z","retry_delay_seconds":3600}`), 0600); err != nil {
		t.Fatal(err)
	}

	restarted := newMarketClient()
	restarted.marketURL = server.URL
	restarted.statePath = statePath
	if err := restarted.loadState(); err != nil {
		t.Fatalf("loadState() error = %v", err)
	}
	got, err := restarted.getListings(t.Context(), false)
	if err != nil {
		t.Fatalf("startup fetch error = %v", err)
	}
	if got.TotalCount != 7 {
		t.Fatalf("startup offers = %d, want 7", got.TotalCount)
	}
	server.Close()
}

func TestMarketRequestDoesNotAdvertiseGzip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "" {
			t.Errorf("Accept-Encoding = %q, want no compression", got)
		}
		_, _ = w.Write([]byte(`{"success":true,"results":[]}`))
	}))
	defer server.Close()

	client := newMarketClient()
	client.marketURL = server.URL
	client.statePath = ""
	if _, err := client.getListings(t.Context(), false); err != nil {
		t.Fatalf("getListings() error = %v", err)
	}
}
