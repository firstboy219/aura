package tools

import (
	"context"
	"errors"
	"testing"
)

const ddgPage = `
<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fwww.vegahotels.id%2Fcikupa&amp;rut=abc">Hotel <b>Vega</b> Cikupa - Harga Kamar</a>
<a class="result__snippet" href="x">Kamar mulai <b>Rp 450.000</b> per malam &amp; kolam renang.</a></div>
<div class="result"><a class="result__a" href="https://duckduckgo.com/y.js?ad_provider=bing&amp;u3=zzz">Iklan</a><a class="result__snippet" href="x">iklan</a></div>
<div class="result"><a class="result__a" href="http://insecure.example/x">Tidak https</a><a class="result__snippet" href="x">http</a></div>
<div class="result"><a class="result__a" href="https://travel.example.com/vega">Review Vega</a><a class="result__snippet" href="x">Ulasan tamu.</a></div>`

func TestParseDuckDuckGoHTML(t *testing.T) {
	got := ParseDuckDuckGoHTML(ddgPage, 5)
	if len(got) != 2 {
		t.Fatalf("want 2 results (ad and http dropped), got %d: %+v", len(got), got)
	}
	if got[0].URL != "https://www.vegahotels.id/cikupa" || got[0].Title != "Hotel Vega Cikupa - Harga Kamar" || got[0].Snippet != "Kamar mulai Rp 450.000 per malam & kolam renang." {
		t.Fatalf("first result wrong: %+v", got[0])
	}
	if one := ParseDuckDuckGoHTML(ddgPage, 1); len(one) != 1 {
		t.Fatalf("n limits the results, got %d", len(one))
	}
}

type fakeSearcher struct {
	name string
	res  []SearchResult
	err  error
}

func (f fakeSearcher) Name() string { return f.name }
func (f fakeSearcher) Search(context.Context, string, int) ([]SearchResult, error) {
	return f.res, f.err
}

func TestFirstWithResults(t *testing.T) {
	hit := []SearchResult{{Title: "a", URL: "https://a.example"}}
	r, err := FirstWithResults{fakeSearcher{"x", nil, errors.New("403")}, fakeSearcher{"y", nil, nil}, fakeSearcher{"z", hit, nil}}.Search(context.Background(), "q", 3)
	if err != nil || len(r) != 1 {
		t.Fatalf("the first searcher with results wins: %v %v", r, err)
	}
	r, err = FirstWithResults{fakeSearcher{"x", nil, errors.New("403")}, fakeSearcher{"y", nil, nil}}.Search(context.Background(), "q", 3)
	if err != nil || r != nil {
		t.Fatalf("an empty answer is not an error: %v %v", r, err)
	}
	if _, err = (FirstWithResults{fakeSearcher{"x", nil, errors.New("403")}}).Search(context.Background(), "q", 3); err == nil {
		t.Fatal("all failing is an error")
	}
}
