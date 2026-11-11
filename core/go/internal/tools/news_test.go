package tools

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

const feedXML = `<?xml version="1.0" encoding="UTF-8"?><rss><channel>
<item><title>Harga &lt;b&gt;emas&lt;/b&gt; naik</title><link>https://www.antaranews.com/a</link><description>&lt;p&gt;Emas naik 1%&lt;/p&gt;</description><pubDate>Wed, 11 Nov 2026 07:00:00 +0700</pubDate></item>
<item><title>Lama</title><link>https://www.antaranews.com/old</link><pubDate>Mon, 02 Nov 2026 07:00:00 +0700</pubDate></item>
<item><title>Script</title><link>javascript:alert(1)</link></item>
<item><title>Kredensial</title><link>https://u:p@evil.example/</link></item>
</channel></rss>`

const googleXML = `<rss><channel><item><title>Nvidia turun - Kontan</title><link>https://news.google.com/rss/articles/x</link>
<description>&lt;a href="x"&gt;Nvidia turun&lt;/a&gt;</description><pubDate>Wed, 11 Nov 2026 01:00:00 GMT</pubDate><source url="https://kontan.co.id">Kontan</source></item></channel></rss>`

func TestParseFeedCleansAndDropsUnsafeLinks(t *testing.T) {
	items, err := ParseFeed(strings.NewReader(feedXML), "ANTARA", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Title != "Harga emas naik" || items[0].Summary != "Emas naik 1%" || items[0].Published.IsZero() {
		t.Fatalf("%+v", items)
	}
	g, _ := ParseFeed(strings.NewReader(googleXML), "Google News", true)
	if len(g) != 1 || g[0].Title != "Nvidia turun" || g[0].Source != "Google News: Kontan" || g[0].Summary != "" {
		t.Fatalf("%+v", g)
	}
	if _, err := ParseFeed(strings.NewReader("not xml"), "x", false); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestHeadlinesMergeFreshItemsAndNameSilentSources(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte(feedXML))
		case "/google":
			w.Write([]byte(googleXML))
		case "/moved":
			http.Redirect(w, r, "https://elsewhere.example/feed", http.StatusFound)
		default:
			http.Error(w, "no", 500)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	g := NewTestGuard(staticResolver{})
	SetTestPort(g, port)
	n := NewNews(g, []Feed{{Name: "ANTARA", URL: "https://feed.test/ok"}, {Name: "Down", URL: "https://feed.test/down"},
		{Name: "Moved", URL: "https://feed.test/moved"}})
	n.google = "x=1"
	n.now = func() time.Time { return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC) }
	tr := srv.Client().Transport.(*http.Transport).TLSClientConfig
	n.tlsConfig = tr.Clone()
	n.tlsConfig.ServerName = "example.com" // the test certificate's name
	items, failed, err := n.Headlines(context.Background(), "", "id")
	if err != nil {
		t.Fatal(err)
	}
	// The old item is dropped (older than 36 h); Google News is not served by this test server path → failed.
	if len(items) != 1 || items[0].Title != "Harga emas naik" {
		t.Fatalf("%+v", items)
	}
	if strings.Join(failed, ",") != "Down,Google News,Moved" {
		t.Fatalf("failed: %v", failed)
	}
}

type staticResolver struct{}

func (staticResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}
