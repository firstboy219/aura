package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Info answers everyday questions from keyless public services (ADR-0063):
// weather, prayer times, the Qur'an, places, routes and market prices. Every
// service is one fixed https host reached through the SSRF guard without
// redirects; answers are untrusted data and become observations.
type Info struct {
	guard     *Guard
	timeout   time.Duration
	loc       *time.Location
	city      string // the owner's default place when a request names none
	now       func() time.Time
	tlsConfig *tls.Config       // tests only
	hosts     map[string]string // tests only: service host → test host
}

func NewInfo(g *Guard, loc *time.Location, city string) *Info {
	if loc == nil {
		loc = time.UTC
	}
	return &Info{guard: g, timeout: 12 * time.Second, loc: loc, city: city, now: time.Now}
}

// InfoTools are the tool ids Info serves.
var InfoTools = []string{"info.weather", "info.prayer", "info.quran", "info.place", "info.route", "info.prices"}

// Run executes one info tool and returns its observation text.
func (in *Info) Run(ctx context.Context, tool string, args map[string]any) (string, error) {
	str := func(k string) string { s, _ := args[k].(string); return strings.TrimSpace(s) }
	num := func(k string) int {
		switch v := args[k].(type) {
		case float64:
			return int(v)
		case string:
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
		return 0
	}
	switch tool {
	case "info.weather":
		return in.Weather(ctx, str("place"), num("days"))
	case "info.prayer":
		return in.Prayer(ctx, str("city"), str("date"))
	case "info.quran":
		return in.Quran(ctx, num("surah"), num("from"), num("to"))
	case "info.place":
		return in.Place(ctx, str("query"))
	case "info.route":
		return in.Route(ctx, str("from"), str("to"), str("mode"))
	case "info.prices":
		var assets []string
		if a, ok := args["assets"].([]any); ok {
			for _, x := range a {
				if s, ok := x.(string); ok {
					assets = append(assets, s)
				}
			}
		}
		return in.Prices(ctx, assets)
	}
	return "", fmt.Errorf("%w: unknown info tool %q", ErrSearch, tool)
}

func (in *Info) get(ctx context.Context, host, path string, q url.Values, v any) error {
	target := host
	if h, ok := in.hosts[host]; ok {
		target = h
	}
	raw := "https://" + target + path
	if len(q) > 0 {
		raw += "?" + q.Encode()
	}
	client := &http.Client{Timeout: in.timeout, Transport: &http.Transport{Proxy: nil, DialContext: in.guard.DialContext, TLSClientConfig: in.tlsConfig},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrSearch, host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%w: %s returned HTTP %d", ErrSearch, host, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(v); err != nil {
		return fmt.Errorf("%w: %s: unreadable answer", ErrSearch, host)
	}
	return nil
}

func (in *Info) place(p string) string {
	if p = strings.TrimSpace(p); p != "" {
		return p
	}
	return in.city
}

// ---------------------------------------------------------------- weather (Open-Meteo)

type geoPoint struct {
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Country   string  `json:"country"`
	Admin1    string  `json:"admin1"`
}

func (in *Info) geocodeCity(ctx context.Context, name string) (*geoPoint, error) {
	var out struct {
		Results []geoPoint `json:"results"`
	}
	if err := in.get(ctx, "geocoding-api.open-meteo.com", "/v1/search", url.Values{"name": {name}, "count": {"1"}, "language": {"id"}}, &out); err != nil {
		return nil, err
	}
	if len(out.Results) == 0 {
		return nil, fmt.Errorf("%w: place %q not found", ErrSearch, name)
	}
	return &out.Results[0], nil
}

var weatherWords = map[int]string{0: "cerah", 1: "cerah berawan", 2: "berawan sebagian", 3: "berawan", 45: "berkabut", 48: "berkabut",
	51: "gerimis ringan", 53: "gerimis", 55: "gerimis lebat", 61: "hujan ringan", 63: "hujan", 65: "hujan lebat",
	80: "hujan lokal ringan", 81: "hujan lokal", 82: "hujan lokal lebat", 95: "badai petir", 96: "badai petir dengan es", 99: "badai petir dengan es"}

func weatherWord(code int) string {
	if w, ok := weatherWords[code]; ok {
		return w
	}
	return fmt.Sprintf("kode cuaca %d", code)
}

func (in *Info) Weather(ctx context.Context, place string, days int) (string, error) {
	place = in.place(place)
	if place == "" {
		return "", fmt.Errorf("%w: no place given and no default city configured", ErrSearch)
	}
	if days < 1 || days > 7 {
		days = 3
	}
	g, err := in.geocodeCity(ctx, place)
	if err != nil {
		return "", err
	}
	var w struct {
		Current struct {
			Time        string  `json:"time"`
			Temperature float64 `json:"temperature_2m"`
			Humidity    float64 `json:"relative_humidity_2m"`
			Code        int     `json:"weather_code"`
			Wind        float64 `json:"wind_speed_10m"`
		} `json:"current"`
		Daily struct {
			Time   []string  `json:"time"`
			Code   []int     `json:"weather_code"`
			Max    []float64 `json:"temperature_2m_max"`
			Min    []float64 `json:"temperature_2m_min"`
			Rain   []float64 `json:"precipitation_probability_max"`
			Sunset []string  `json:"sunset"`
		} `json:"daily"`
	}
	q := url.Values{"latitude": {ff(g.Latitude)}, "longitude": {ff(g.Longitude)}, "timezone": {in.loc.String()}, "forecast_days": {strconv.Itoa(days)},
		"current": {"temperature_2m,relative_humidity_2m,weather_code,wind_speed_10m"},
		"daily":   {"weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max,sunset"}}
	if err := in.get(ctx, "api.open-meteo.com", "/v1/forecast", q, &w); err != nil {
		return "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Weather for %s (%s), source Open-Meteo:\n", g.Name, strings.Trim(g.Admin1+", "+g.Country, ", "))
	fmt.Fprintf(&sb, "Now (%s): %s, %.0f°C, humidity %.0f%%, wind %.0f km/h\n", w.Current.Time, weatherWord(w.Current.Code), w.Current.Temperature, w.Current.Humidity, w.Current.Wind)
	for i, d := range w.Daily.Time {
		if i >= len(w.Daily.Code) || i >= len(w.Daily.Max) || i >= len(w.Daily.Min) {
			break
		}
		rain := ""
		if i < len(w.Daily.Rain) {
			rain = fmt.Sprintf(", chance of rain %.0f%%", w.Daily.Rain[i])
		}
		fmt.Fprintf(&sb, "%s: %s, %.0f–%.0f°C%s\n", d, weatherWord(w.Daily.Code[i]), w.Daily.Min[i], w.Daily.Max[i], rain)
	}
	return sb.String(), nil
}

func ff(f float64) string { return strconv.FormatFloat(f, 'f', 4, 64) }

// ---------------------------------------------------------------- prayer times (Kemenag schedule via myquran.com)

var reDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func (in *Info) Prayer(ctx context.Context, city, date string) (string, error) {
	city = in.place(city)
	if city == "" {
		return "", fmt.Errorf("%w: no city given and no default city configured", ErrSearch)
	}
	if date == "" {
		date = in.now().In(in.loc).Format("2006-01-02")
	}
	if !reDate.MatchString(date) {
		return "", fmt.Errorf("%w: date must be YYYY-MM-DD", ErrSearch)
	}
	var found struct {
		Data []struct {
			ID     string `json:"id"`
			Lokasi string `json:"lokasi"`
		} `json:"data"`
	}
	key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(city), "kota "), "kabupaten ")))
	if err := in.get(ctx, "api.myquran.com", "/v2/sholat/kota/cari/"+url.PathEscape(key), nil, &found); err != nil {
		return "", err
	}
	if len(found.Data) == 0 {
		return "", fmt.Errorf("%w: city %q is not in the Kemenag list", ErrSearch, city)
	}
	pick := found.Data[0]
	for _, d := range found.Data {
		if strings.HasPrefix(d.Lokasi, "KOTA ") {
			pick = d
			break
		}
	}
	var out struct {
		Data struct {
			Lokasi string            `json:"lokasi"`
			Daerah string            `json:"daerah"`
			Jadwal map[string]string `json:"jadwal"`
		} `json:"data"`
	}
	if err := in.get(ctx, "api.myquran.com", "/v2/sholat/jadwal/"+url.PathEscape(pick.ID)+"/"+date, nil, &out); err != nil {
		return "", err
	}
	j := out.Data.Jadwal
	if j["subuh"] == "" {
		return "", fmt.Errorf("%w: no schedule returned", ErrSearch)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Prayer times (Kemenag schedule) for %s, %s — %s:\n", out.Data.Lokasi, out.Data.Daerah, j["tanggal"])
	for _, k := range []string{"imsak", "subuh", "terbit", "dhuha", "dzuhur", "ashar", "maghrib", "isya"} {
		if v := j[k]; v != "" {
			fmt.Fprintf(&sb, "%s %s\n", k, v)
		}
	}
	if next := nextPrayer(j, in.now().In(in.loc), date); next != "" {
		sb.WriteString(next + "\n")
	}
	return sb.String(), nil
}

func nextPrayer(j map[string]string, now time.Time, date string) string {
	if now.Format("2006-01-02") != date {
		return ""
	}
	for _, k := range []string{"subuh", "dzuhur", "ashar", "maghrib", "isya"} {
		t, err := time.ParseInLocation("2006-01-02 15:04", date+" "+j[k], now.Location())
		if err == nil && t.After(now) {
			m := int(math.Ceil(t.Sub(now).Minutes()))
			return fmt.Sprintf("Next: %s at %s (in %d h %d min)", k, j[k], m/60, m%60)
		}
	}
	return ""
}

// ---------------------------------------------------------------- Qur'an (equran.id, Kemenag translation)

func (in *Info) Quran(ctx context.Context, surah, from, to int) (string, error) {
	if surah < 1 || surah > 114 {
		return "", fmt.Errorf("%w: surah must be 1..114", ErrSearch)
	}
	var out struct {
		Data struct {
			Nama      string `json:"nama"`
			NamaLatin string `json:"namaLatin"`
			Jumlah    int    `json:"jumlahAyat"`
			Arti      string `json:"arti"`
			Tempat    string `json:"tempatTurun"`
			Ayat      []struct {
				Nomor     int    `json:"nomorAyat"`
				Arab      string `json:"teksArab"`
				Latin     string `json:"teksLatin"`
				Indonesia string `json:"teksIndonesia"`
			} `json:"ayat"`
		} `json:"data"`
	}
	if err := in.get(ctx, "equran.id", "/api/v2/surat/"+strconv.Itoa(surah), nil, &out); err != nil {
		return "", err
	}
	d := out.Data
	if from < 1 {
		from = 1
	}
	if to < from || to > d.Jumlah {
		to = d.Jumlah
	}
	if to-from > 29 {
		to = from + 29 // at most 30 verses per observation
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Surah %d %s (%s, \"%s\"), %d verses, %s. Text and Kemenag translation from equran.id. Verses %d–%d:\n",
		surah, d.NamaLatin, d.Nama, d.Arti, d.Jumlah, d.Tempat, from, to)
	for _, a := range d.Ayat {
		if a.Nomor < from || a.Nomor > to {
			continue
		}
		fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n", a.Nomor, a.Arab, a.Latin, a.Indonesia)
	}
	return sb.String(), nil
}

// ---------------------------------------------------------------- places and routes (OpenStreetMap)

type osmPlace struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
	Category    string `json:"category"`
	Type        string `json:"type"`
}

func (in *Info) search(ctx context.Context, q string, n int) ([]osmPlace, error) {
	if q == "" || len(q) > 200 {
		return nil, fmt.Errorf("%w: a place query of 1..200 characters is needed", ErrSearch)
	}
	var out []osmPlace
	err := in.get(ctx, "nominatim.openstreetmap.org", "/search", url.Values{"q": {q}, "format": {"jsonv2"}, "limit": {strconv.Itoa(n)},
		"accept-language": {"id"}, "countrycodes": {"id"}}, &out)
	if err == nil && len(out) == 0 {
		// Not in Indonesia: try worldwide.
		err = in.get(ctx, "nominatim.openstreetmap.org", "/search", url.Values{"q": {q}, "format": {"jsonv2"}, "limit": {strconv.Itoa(n)},
			"accept-language": {"id"}}, &out)
	}
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: place %q not found", ErrSearch, q)
	}
	return out, nil
}

func (in *Info) Place(ctx context.Context, q string) (string, error) {
	ps, err := in.search(ctx, q, 5)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Places matching %q (OpenStreetMap):\n", q)
	for i, p := range ps {
		fmt.Fprintf(&sb, "%d. %s (%s/%s) at %s,%s\n   https://www.openstreetmap.org/?mlat=%s&mlon=%s#map=17/%s/%s\n", i+1, p.DisplayName, p.Category, p.Type,
			p.Lat, p.Lon, p.Lat, p.Lon, p.Lat, p.Lon)
	}
	return sb.String(), nil
}

// trafficFactor turns free-flow driving time into an honest estimate for
// Jakarta-area roads by time of day (the router knows no live traffic).
func trafficFactor(t time.Time) float64 {
	h := t.Hour()
	switch {
	case t.Weekday() == time.Saturday || t.Weekday() == time.Sunday:
		return 1.3
	case h >= 6 && h < 10, h >= 16 && h < 20:
		return 2.0
	case h >= 10 && h < 16:
		return 1.5
	}
	return 1.2
}

func (in *Info) Route(ctx context.Context, from, to, mode string) (string, error) {
	from = in.place(from)
	if from == "" || to == "" {
		return "", fmt.Errorf("%w: a route needs from and to", ErrSearch)
	}
	profile, host := "driving", "router.project-osrm.org"
	switch mode {
	case "", "car", "driving", "motor", "motorcycle":
	case "walk", "walking", "foot":
		profile, host = "foot", "routing.openstreetmap.de"
	case "bike", "cycling", "bicycle":
		profile, host = "bike", "routing.openstreetmap.de"
	default:
		return "", fmt.Errorf("%w: mode must be car, motorcycle, walk or bike", ErrSearch)
	}
	a, err := in.search(ctx, from, 1)
	if err != nil {
		return "", err
	}
	b, err := in.search(ctx, to, 1)
	if err != nil {
		return "", err
	}
	path := "/route/v1/" + profile + "/" + a[0].Lon + "," + a[0].Lat + ";" + b[0].Lon + "," + b[0].Lat
	if host == "routing.openstreetmap.de" {
		path = "/routed-" + profile + path
	}
	var out struct {
		Code   string `json:"code"`
		Routes []struct {
			Duration float64 `json:"duration"`
			Distance float64 `json:"distance"`
		} `json:"routes"`
	}
	if err := in.get(ctx, host, path, url.Values{"overview": {"false"}}, &out); err != nil {
		return "", err
	}
	if out.Code != "Ok" || len(out.Routes) == 0 {
		return "", fmt.Errorf("%w: no route found", ErrSearch)
	}
	r := out.Routes[0]
	free := r.Duration / 60
	var sb strings.Builder
	fmt.Fprintf(&sb, "Route (OpenStreetMap/OSRM, %s) from %s to %s: %.1f km, %.0f min without traffic.\n", profile, a[0].DisplayName, b[0].DisplayName, r.Distance/1000, free)
	if profile == "driving" {
		now := in.now().In(in.loc)
		f := trafficFactor(now)
		fmt.Fprintf(&sb, "Estimate with typical traffic at %s: about %.0f min (factor %.1f; no live traffic data).\n", now.Format("15:04"), free*f, f)
	}
	fmt.Fprintf(&sb, "Map: https://www.openstreetmap.org/directions?engine=fossgis_osrm_car&route=%s%%2C%s%%3B%s%%2C%s\n", a[0].Lat, a[0].Lon, b[0].Lat, b[0].Lon)
	return sb.String(), nil
}

// ---------------------------------------------------------------- market prices (CoinGecko, ExchangeRate-API)

var coinIDs = map[string]string{"bitcoin": "bitcoin", "btc": "bitcoin", "ethereum": "ethereum", "eth": "ethereum", "solana": "solana", "sol": "solana",
	"bnb": "binancecoin", "xrp": "ripple", "usdt": "tether", "tether": "tether", "doge": "dogecoin", "dogecoin": "dogecoin",
	"gold": "tether-gold", "emas": "tether-gold", "xau": "tether-gold"}

var reCurrency = regexp.MustCompile(`^[A-Za-z]{3}$`)

func (in *Info) Prices(ctx context.Context, assets []string) (string, error) {
	if len(assets) == 0 {
		assets = []string{"bitcoin", "gold", "USD"}
	}
	if len(assets) > 10 {
		assets = assets[:10]
	}
	var coins, fx []string
	for _, a := range assets {
		k := strings.ToLower(strings.TrimSpace(a))
		if id, ok := coinIDs[k]; ok {
			coins = append(coins, id)
		} else if reCurrency.MatchString(k) {
			fx = append(fx, strings.ToUpper(k))
		} else {
			return "", fmt.Errorf("%w: unknown asset %q (crypto name, gold, or a 3-letter currency)", ErrSearch, a)
		}
	}
	var sb strings.Builder
	stamp := in.now().In(in.loc).Format("2 Jan 2006 15:04")
	if len(coins) > 0 {
		var out map[string]map[string]float64
		if err := in.get(ctx, "api.coingecko.com", "/api/v3/simple/price", url.Values{"ids": {strings.Join(coins, ",")}, "vs_currencies": {"idr,usd"},
			"include_24hr_change": {"true"}}, &out); err != nil {
			return "", err
		}
		fmt.Fprintf(&sb, "Prices from CoinGecko at %s:\n", stamp)
		for _, id := range coins {
			p, ok := out[id]
			if !ok {
				continue
			}
			label := id
			if id == "tether-gold" {
				label = "gold (XAUT, one troy ounce = 31.1 g; per gram ≈ Rp " + rupiah(p["idr"]/31.1035) + ")"
			}
			fmt.Fprintf(&sb, "- %s: Rp %s / US$ %s, 24h %+.2f%%\n", label, rupiah(p["idr"]), strconv.FormatFloat(p["usd"], 'f', 2, 64), p["idr_24h_change"])
		}
	}
	if len(fx) > 0 {
		var out struct {
			Result string             `json:"result"`
			Rates  map[string]float64 `json:"rates"`
			Update string             `json:"time_last_update_utc"`
		}
		if err := in.get(ctx, "open.er-api.com", "/v6/latest/IDR", nil, &out); err != nil {
			return "", err
		}
		fmt.Fprintf(&sb, "Exchange rates from ExchangeRate-API (updated %s):\n", out.Update)
		for _, c := range fx {
			r := out.Rates[c]
			if r <= 0 {
				fmt.Fprintf(&sb, "- %s: not available\n", c)
				continue
			}
			fmt.Fprintf(&sb, "- 1 %s = Rp %s\n", c, rupiah(1/r))
		}
	}
	return sb.String(), nil
}

// rupiah formats an amount with Indonesian thousands separators.
func rupiah(v float64) string {
	n := int64(math.Round(v))
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
