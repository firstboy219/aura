package tools

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testInfo(t *testing.T, h http.HandlerFunc) *Info {
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	g := NewTestGuard(staticResolver{})
	SetTestPort(g, port)
	loc, _ := time.LoadLocation("Asia/Jakarta")
	in := NewInfo(g, loc, "Jakarta")
	in.now = func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, loc) }
	in.tlsConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	in.tlsConfig.ServerName = "example.com"
	in.hosts = map[string]string{}
	for _, h := range []string{"geocoding-api.open-meteo.com", "api.open-meteo.com", "api.myquran.com", "equran.id", "nominatim.openstreetmap.org",
		"router.project-osrm.org", "routing.openstreetmap.de", "api.coingecko.com", "open.er-api.com"} {
		in.hosts[h] = "example.com"
	}
	return in
}

func TestInfoServices(t *testing.T) {
	var paths []string
	in := testInfo(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case r.URL.Path == "/v1/search":
			w.Write([]byte(`{"results":[{"name":"Jakarta","latitude":-6.2,"longitude":106.8,"country":"Indonesia","admin1":"DKI Jakarta"}]}`))
		case r.URL.Path == "/v1/forecast":
			w.Write([]byte(`{"current":{"time":"2026-11-11T10:00","temperature_2m":31.4,"relative_humidity_2m":70,"weather_code":63,"wind_speed_10m":9},
				"daily":{"time":["2026-11-11"],"weather_code":[95],"temperature_2m_max":[33],"temperature_2m_min":[25],"precipitation_probability_max":[80]}}`))
		case strings.HasPrefix(r.URL.Path, "/v2/sholat/kota/cari/"):
			w.Write([]byte(`{"data":[{"id":"1101","lokasi":"KAB. JAKARTA"},{"id":"1301","lokasi":"KOTA JAKARTA"}]}`))
		case r.URL.Path == "/v2/sholat/jadwal/1301/2026-11-11":
			w.Write([]byte(`{"data":{"lokasi":"KOTA JAKARTA","daerah":"DKI JAKARTA","jadwal":{"tanggal":"Rabu, 11/11/2026","imsak":"03:56","subuh":"04:06","dzuhur":"11:42","ashar":"15:03","maghrib":"17:56","isya":"19:08"}}}`))
		case r.URL.Path == "/api/v2/surat/112":
			w.Write([]byte(`{"data":{"nama":"الإخلاص","namaLatin":"Al-Ikhlas","jumlahAyat":4,"arti":"Ikhlas","tempatTurun":"Mekah","ayat":[
				{"nomorAyat":1,"teksArab":"قُلْ هُوَ اللّٰهُ اَحَدٌ","teksLatin":"qul huwallāhu aḥad","teksIndonesia":"Katakanlah (Nabi Muhammad), “Dialah Allah Yang Maha Esa."},
				{"nomorAyat":2,"teksArab":"اَللّٰهُ الصَّمَدُ","teksLatin":"allāhuṣ-ṣamad","teksIndonesia":"Allah tempat meminta segala sesuatu."}]}}`))
		case r.URL.Path == "/search":
			if r.URL.Query().Get("q") == "Stasiun Manggarai" {
				w.Write([]byte(`[{"display_name":"Stasiun Manggarai, Jakarta","lat":"-6.2101","lon":"106.8499","category":"railway","type":"station"}]`))
			} else {
				w.Write([]byte(`[{"display_name":"Monas, Jakarta","lat":"-6.1754","lon":"106.8272","category":"tourism","type":"attraction"}]`))
			}
		case strings.HasPrefix(r.URL.Path, "/route/v1/driving/"):
			w.Write([]byte(`{"code":"Ok","routes":[{"duration":600,"distance":5000}]}`))
		case r.URL.Path == "/api/v3/simple/price":
			w.Write([]byte(`{"bitcoin":{"idr":1338775559,"usd":73693,"idr_24h_change":-1.25},"tether-gold":{"idr":73403632,"usd":4040.53}}`))
		case r.URL.Path == "/v6/latest/IDR":
			w.Write([]byte(`{"result":"success","rates":{"USD":0.0000606,"SGD":0.0000785},"time_last_update_utc":"Wed, 11 Nov 2026 00:02:31 +0000"}`))
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	check := func(tool string, args map[string]any, want ...string) {
		t.Helper()
		out, err := in.Run(ctx, tool, args)
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: %q missing in:\n%s", tool, w, out)
			}
		}
	}
	check("info.weather", map[string]any{}, "Weather for Jakarta", "hujan, 31°C", "badai petir, 25–33°C, chance of rain 80%")
	check("info.prayer", map[string]any{"city": ""}, "KOTA JAKARTA", "subuh 04:06", "maghrib 17:56", "Next: dzuhur at 11:42 (in 1 h 42 min)")
	check("info.quran", map[string]any{"surah": float64(112), "from": float64(1), "to": float64(9)}, "Al-Ikhlas", "Verses 1–4", "2. اَللّٰهُ الصَّمَدُ")
	check("info.place", map[string]any{"query": "Monas"}, "Monas, Jakarta", "openstreetmap.org/?mlat=-6.1754")
	check("info.route", map[string]any{"from": "Stasiun Manggarai", "to": "Monas", "mode": "car"}, "5.0 km, 10 min without traffic", "about 15 min (factor 1.5")
	check("info.prices", map[string]any{"assets": []any{"btc", "emas", "usd"}}, "bitcoin: Rp 1.338.775.559", "24h -1.25%", "per gram ≈ Rp 2.359.980", "1 USD = Rp 16.502")
	for _, bad := range []struct {
		tool string
		args map[string]any
	}{
		{"info.quran", map[string]any{"surah": float64(115)}},
		{"info.prayer", map[string]any{"date": "besok"}},
		{"info.route", map[string]any{"to": "Monas", "mode": "plane"}},
		{"info.prices", map[string]any{"assets": []any{"saham bbca"}}},
		{"info.unknown", nil},
	} {
		if _, err := in.Run(ctx, bad.tool, bad.args); err == nil {
			t.Errorf("%s %v accepted", bad.tool, bad.args)
		}
	}
}

func TestRupiah(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 999: "999", 1000: "1.000", 16502.4: "16.502", 1338775559: "1.338.775.559", -2500: "-2.500"} {
		if got := rupiah(in); got != want {
			t.Errorf("rupiah(%v) = %s, want %s", in, got, want)
		}
	}
}
