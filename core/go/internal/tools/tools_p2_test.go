package tools

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCalcExact(t *testing.T) {
	cases := map[string]string{
		"0.1 + 0.2":                 "0.3",
		"(1250000 * 3) / 4":         "937500",
		"2^10 - 1":                  "1023",
		"-3 + 2 * (4 - 1)":          "3",
		"1/3 + 1/3 + 1/3":           "1",
		"round(2/3, 4)":             "0.6667",
		"round(-2.5, 0)":            "-3",
		"max(1, 7.5, 3) - min(4,2)": "5.5",
		"abs(-12.75)":               "12.75",
		"1e3 + 1_000":               "2000",
		"2^-2":                      "0.25",
	}
	for in, want := range cases {
		r, err := Calc(in)
		if err != nil || r.Result != want {
			t.Errorf("%s = %v (%v), want %s", in, r, err, want)
		}
	}
	r, _ := Calc("1/3")
	if r.Exact || r.Fraction != "1/3" {
		t.Errorf("1/3: %+v", r)
	}
	r, _ = Calc("sqrt(16)")
	if r.Result != "4" || !r.Exact {
		t.Errorf("sqrt(16): %+v", r)
	}
	r, _ = Calc("sqrt(2)")
	if r.Exact || !strings.HasPrefix(r.Result, "1.414213562373095048") {
		t.Errorf("sqrt(2): %+v", r)
	}
	for _, bad := range []string{"", "1/0", "2^0.5", "2^100000", "import os", "1 +", "((((((((((((((((((((((((((((((((((((((((((((((((((((1))))))))))))))))))))))))))))))))))))))))))))))))))))", "sqrt(-1)", strings.Repeat("1+", 300) + "1"} {
		if _, err := Calc(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestReadDataExactStats(t *testing.T) {
	w := ws(t)
	w.Write("T", "sales.csv", "region,amount,note\nJakarta,1250000.50,a\nBandung,980000.25,b\nSurabaya,,c\n")
	s, err := w.ReadData("T", "sales.csv")
	if err != nil {
		t.Fatal(err)
	}
	a := s.Numeric["amount"]
	if s.RowCount != 3 || a.Count != 2 || a.Sum != "2230000.75" || a.Mean != "1115000.375" || a.Min != "980000.25" {
		t.Fatalf("%+v %+v", s, a)
	}
	if _, ok := s.Numeric["region"]; ok {
		t.Fatal("text column treated as numeric")
	}
	w.Write("T", "items.json", `[{"name":"a","qty":2},{"name":"b","qty":3.5}]`)
	j, err := w.ReadData("T", "items.json")
	if err != nil || j.Numeric["qty"].Sum != "5.5" || j.RowCount != 2 {
		t.Fatalf("%v %+v", err, j)
	}
	os.WriteFile(filepath.Join(w.Root(), "T", "x.bin"), []byte("x"), 0o600)
	if _, err := w.ReadData("T", "x.bin"); err == nil {
		t.Fatal("binary accepted")
	}
}

func TestExtractLinks(t *testing.T) {
	base, _ := url.Parse("https://a.test/docs/page.html")
	got := ExtractLinks(`<a href="/x">x</a> <a class=y href='other.html#frag'>o</a> <a href="javascript:alert(1)">j</a>
		<a href="https://b.test/p?q=1">b</a> <a href="#top">t</a> <a href="/x">dup</a> <a href="https://u:p@c.test/">c</a>`, base)
	want := "https://a.test/x,https://a.test/docs/other.html,https://b.test/p?q=1"
	if strings.Join(got, ",") != want {
		t.Fatal(got)
	}
}
