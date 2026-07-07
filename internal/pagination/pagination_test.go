package pagination

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParse_Defaults(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/tenants", nil)
	p := Parse(r)
	if p.Limit != DefaultLimit {
		t.Errorf("default limit = %d, want %d", p.Limit, DefaultLimit)
	}
	if p.Offset != 0 {
		t.Errorf("default offset = %d, want 0", p.Offset)
	}
}

func TestParse_ExplicitValues(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/tenants?limit=10&offset=20", nil)
	p := Parse(r)
	if p.Limit != 10 {
		t.Errorf("limit = %d, want 10", p.Limit)
	}
	if p.Offset != 20 {
		t.Errorf("offset = %d, want 20", p.Offset)
	}
}

func TestParse_ClampsMax(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/tenants?limit=99999", nil)
	p := Parse(r)
	if p.Limit != MaxLimit {
		t.Errorf("limit = %d, want clamped to %d", p.Limit, MaxLimit)
	}
}

func TestParse_InvalidFallsBack(t *testing.T) {
	cases := []string{
		"?limit=-5&offset=-1",
		"?limit=abc&offset=xyz",
		"?limit=0", // 0 → default, not "unlimited"
	}
	for _, q := range cases {
		r := httptest.NewRequest("GET", "/api/v1/tenants"+q, nil)
		p := Parse(r)
		if p.Limit != DefaultLimit {
			t.Errorf("q=%q: limit = %d, want default %d", q, p.Limit, DefaultLimit)
		}
		if p.Offset != 0 {
			t.Errorf("q=%q: offset = %d, want 0", q, p.Offset)
		}
	}
}

func TestRemainingItemCount(t *testing.T) {
	cases := []struct {
		total, offset, returned, want int
	}{
		{100, 0, 50, 50}, // first page of 50, 50 more
		{100, 50, 50, 0}, // last page
		{100, 90, 10, 0}, // partial last page
		{5, 0, 5, 0},     // single page, exact
		{0, 0, 0, 0},     // empty
		{10, 20, 0, 0},   // offset past end → clamped to 0
	}
	for _, c := range cases {
		got := RemainingItemCount(c.total, c.offset, c.returned)
		if got != c.want {
			t.Errorf("RemainingItemCount(%d,%d,%d) = %d, want %d", c.total, c.offset, c.returned, got, c.want)
		}
	}
}

// TestParse_UsedInRequest confirms Parse works on a real *http.Request (not
// just httptest), matching how handlers call it.
func TestParse_UsedInRequest(t *testing.T) {
	r, _ := http.NewRequest("GET", "/api/v1/machines?limit=25&offset=50", nil)
	p := Parse(r)
	if p.Limit != 25 || p.Offset != 50 {
		t.Errorf("got limit=%d offset=%d", p.Limit, p.Offset)
	}
}
