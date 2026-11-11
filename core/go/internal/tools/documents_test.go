package tools

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

const sampleMD = `# Laporan Penjualan

Penjualan **naik 18%** dibanding kuartal lalu.

## Rincian
- Jawa: Rp 2,1 M
- Sumatra: Rp 1,0 M

| Wilayah | Omzet |
|---|---|
| Jawa | 2,1 M |
| Bali | 0,4 M |
`

func TestDocumentsRoundTrip(t *testing.T) {
	for _, p := range []string{"reports/laporan.docx", "reports/laporan.pdf"} {
		b, err := RenderDocument(p, sampleMD)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		again, _ := RenderDocument(p, sampleMD)
		if !bytes.Equal(b, again) {
			t.Fatalf("%s: rendering is not deterministic", p)
		}
		text, err := ExtractText(p, b)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		for _, want := range []string{"Laporan Penjualan", "naik 18%", "Sumatra: Rp 1,0 M", "Bali"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: %q missing in:\n%s", p, want, text)
			}
		}
	}
	x, err := RenderDocument("data/omzet.xlsx", "Wilayah,Omzet,Catatan\nJawa,2100000000,\"besar, stabil\"\nBali,400000000,=HYPERLINK(\"http://evil\")\n")
	if err != nil {
		t.Fatal(err)
	}
	text, err := ExtractText("data/omzet.xlsx", x)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Jawa\t2100000000\tbesar, stabil") || !strings.Contains(text, "'=HYPERLINK") {
		t.Fatalf("xlsx:\n%s", text)
	}
	if strings.Contains(string(x), "<f>") {
		t.Fatal("a formula was written")
	}
}

func TestDocumentReadingLimits(t *testing.T) {
	if _, err := ExtractText("a.docx", []byte("not a zip")); !errors.Is(err, ErrDocument) {
		t.Fatalf("garbage: %v", err)
	}
	if _, err := ExtractText("a.pdf", []byte("hello")); !errors.Is(err, ErrDocument) {
		t.Fatalf("not a pdf: %v", err)
	}
	// A compressed part that expands beyond the limit is refused (zip bomb).
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, _ := z.Create("word/document.xml")
	w.Write(bytes.Repeat([]byte("A"), maxDocEntry+10))
	z.Close()
	if _, err := ExtractText("big.docx", buf.Bytes()); !errors.Is(err, ErrDocument) {
		t.Fatalf("bomb: %v", err)
	}
	if Rendered("a.md") || !Rendered("a.PDF") || !Extractable("x.pptx") || Extractable("x.png") {
		t.Fatal("type checks")
	}
}
