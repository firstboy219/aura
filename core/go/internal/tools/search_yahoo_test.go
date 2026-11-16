package tools

import (
	"context"
	"errors"
	"testing"
)

const yahooPage = `
<div class="dd algo algo-sr relsrch richAlgo" data-yga='{}'><div class="compTitle options-toggle"><h3 style="display:block" class="title"><a style="x" class="d-ib" href="https://r.search.yahoo.com/_ylt=A2/RV=2/RE=1/RO=10/RU=https%3a%2f%2fwww.agoda.com%2fid-id%2fara-hotel-gading-serpong%2fhotel%2ftangerang-id.html/RK=2/RS=abc-" aria-label="Vega Gading Serpong - Agoda"><span class="d-ib">www.agoda.com</span>Vega Gading Serpong - Agoda</a></h3></div><div class="compText aAbs"><p class="fz-14"> <span><b>Hotel</b> ini punya <b>kolam</b> renang &amp; gym.</span> </p></div></div>
<div class="dd algo algo-sr" data-yga='{}'><div class="compTitle"><h3 class="title"><a class="d-ib" href="https://www.bing.com/aclick?ld=e8Rt" aria-label="Iklan">Iklan</a></h3></div><div class="compText"><p>iklan</p></div></div>
<div class="dd algo algo-sr" data-yga='{}'><div class="compTitle"><h3 class="title"><a class="d-ib" href="https://r.search.yahoo.com/_ylt=A/RU=https%3a%2f%2fwww.tiket.com%2fid-id%2fhotel%2fvega/RK=2/RS=x-" aria-label="Vega Hotel - tiket.com">Vega Hotel - tiket.com</a></h3></div><div class="compText"><p><span>Diskon hotel Tangerang.</span></p></div></div>
<div class="dd algo algo-sr" data-yga='{}'><div class="compTitle"><h3 class="title"><a class="d-ib" href="https://r.search.yahoo.com/_ylt=A/RU=http%3a%2f%2finsecure.example%2fx/RK=2/RS=x-" aria-label="Tanpa https">Tanpa https</a></h3></div></div>`

func TestParseYahooHTML(t *testing.T) {
	got := ParseYahooHTML(yahooPage, 8)
	if len(got) != 2 {
		t.Fatalf("want 2 results (ad and http dropped), got %d: %+v", len(got), got)
	}
	if got[0].URL != "https://www.agoda.com/id-id/ara-hotel-gading-serpong/hotel/tangerang-id.html" || got[0].Title != "Vega Gading Serpong - Agoda" || got[0].Snippet != "Hotel ini punya kolam renang & gym." {
		t.Fatalf("first result wrong: %+v", got[0])
	}
	if got[1].URL != "https://www.tiket.com/id-id/hotel/vega" {
		t.Fatalf("second result wrong: %+v", got[1])
	}
	if one := ParseYahooHTML(yahooPage, 1); len(one) != 1 {
		t.Fatalf("n limits the results, got %d", len(one))
	}
	if none := ParseYahooHTML("<html>kosong</html>", 5); len(none) != 0 {
		t.Fatalf("no results expected, got %+v", none)
	}
}

func TestMergedTakesTurnsAndListsAPageOnce(t *testing.T) {
	a := fakeSearcher{"a", []SearchResult{{Title: "A1", URL: "https://www.agoda.com/x/"}, {Title: "A2", URL: "https://a.example/2"}}, nil}
	b := fakeSearcher{"b", []SearchResult{{Title: "B1", URL: "https://agoda.com/x"}, {Title: "B2", URL: "https://b.example/2"}, {Title: "B3", URL: "https://b.example/3"}}, nil}
	r, err := Merged{a, b}.Search(context.Background(), "q", 4)
	if err != nil || len(r) != 4 || r[0].Title != "A1" || r[1].Title != "A2" || r[2].Title != "B2" || r[3].Title != "B3" {
		t.Fatalf("turns, once each: %+v %v", r, err)
	}
	bad := fakeSearcher{"bad", nil, errors.New("403")}
	if r, err = (Merged{bad, a}).Search(context.Background(), "q", 3); err != nil || len(r) != 2 {
		t.Fatalf("one failing costs nothing: %+v %v", r, err)
	}
	if _, err = (Merged{bad, bad}).Search(context.Background(), "q", 3); err == nil {
		t.Fatal("all failing is an error")
	}
}
