package tools

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Office documents (ADR-0064). workspace.write turns Markdown into .docx and
// .pdf and CSV into .xlsx; workspace.read turns .docx, .xlsx, .pptx and .pdf
// back into plain text. Only the standard library is used: the formats are
// written from fixed templates and read as data (no macros, no scripts, no
// external references), and every archive is read with size limits.

// ErrDocument means a document could not be made or read.
var ErrDocument = errors.New("document_error")

const (
	maxDocEntry = 8 << 20  // one part of an archive, uncompressed
	maxDocTotal = 24 << 20 // all parts read from one archive
	maxDocText  = 400_000  // characters of extracted text
)

// docEpoch is the fixed timestamp of every generated archive entry, so the
// same content always gives the same bytes (idempotent writes).
var docEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Rendered reports whether workspace.write renders content for this file type.
func Rendered(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".docx", ".xlsx", ".pdf":
		return true
	}
	return false
}

// Extractable reports whether workspace.read extracts text from this file type.
func Extractable(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".docx", ".xlsx", ".pptx", ".pdf":
		return true
	}
	return false
}

// RenderDocument makes the file bytes for p from content: Markdown for .docx
// and .pdf, CSV for .xlsx.
func RenderDocument(p, content string) ([]byte, error) {
	switch strings.ToLower(path.Ext(p)) {
	case ".docx":
		return renderDOCX(content)
	case ".xlsx":
		return renderXLSX(content)
	case ".pdf":
		return renderPDF(content)
	}
	return nil, fmt.Errorf("%w: %s is not a rendered type", ErrDocument, path.Ext(p))
}

// ---------------------------------------------------------------- Markdown lines

type mdLine struct {
	kind  string // h1 h2 h3 p li ol tr hr
	text  string
	cells []string
}

var reOrdered = regexp.MustCompile(`^\d+[.)]\s+`)

func parseMarkdown(md string) []mdLine {
	var out []mdLine
	for _, raw := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		l := strings.TrimRight(raw, " \t")
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			continue
		case strings.HasPrefix(t, "### "):
			out = append(out, mdLine{kind: "h3", text: t[4:]})
		case strings.HasPrefix(t, "## "):
			out = append(out, mdLine{kind: "h2", text: t[3:]})
		case strings.HasPrefix(t, "# "):
			out = append(out, mdLine{kind: "h1", text: t[2:]})
		case t == "---" || t == "***":
			out = append(out, mdLine{kind: "hr"})
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
			out = append(out, mdLine{kind: "li", text: t[2:]})
		case reOrdered.MatchString(t):
			out = append(out, mdLine{kind: "ol", text: t})
		case strings.HasPrefix(t, "|") && strings.HasSuffix(t, "|"):
			cells := strings.Split(strings.Trim(t, "|"), "|")
			sep := true
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
				if strings.Trim(cells[i], ":-") != "" {
					sep = false
				}
			}
			if !sep {
				out = append(out, mdLine{kind: "tr", cells: cells})
			}
		default:
			out = append(out, mdLine{kind: "p", text: t})
		}
	}
	return out
}

// runs splits **bold** spans.
func runs(s string) []struct {
	text string
	bold bool
} {
	var out []struct {
		text string
		bold bool
	}
	parts := strings.Split(s, "**")
	for i, p := range parts {
		if p != "" {
			out = append(out, struct {
				text string
				bold bool
			}{p, i%2 == 1})
		}
	}
	return out
}

func xmlText(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		if r == '\t' || r == '\n' || r >= 0x20 && r != 0xFFFE && r != 0xFFFF {
			xml.EscapeText(&b, []byte(string(r)))
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- DOCX

func writeZip(files [][2]string) ([]byte, error) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := z.CreateHeader(&zip.FileHeader{Name: f[0], Method: zip.Deflate, Modified: docEpoch})
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, f[1]); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func renderDOCX(md string) ([]byte, error) {
	var body strings.Builder
	para := func(style, text string, prefix string) {
		body.WriteString("<w:p>")
		if style != "" {
			fmt.Fprintf(&body, `<w:pPr><w:pStyle w:val="%s"/></w:pPr>`, style)
		}
		if prefix != "" {
			fmt.Fprintf(&body, `<w:r><w:t xml:space="preserve">%s</w:t></w:r>`, xmlText(prefix))
		}
		for _, r := range runs(text) {
			body.WriteString("<w:r>")
			if r.bold {
				body.WriteString("<w:rPr><w:b/></w:rPr>")
			}
			fmt.Fprintf(&body, `<w:t xml:space="preserve">%s</w:t></w:r>`, xmlText(r.text))
		}
		body.WriteString("</w:p>")
	}
	lines := parseMarkdown(md)
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch l.kind {
		case "h1":
			para("Heading1", l.text, "")
		case "h2":
			para("Heading2", l.text, "")
		case "h3":
			para("Heading3", l.text, "")
		case "li":
			para("ListParagraph", l.text, "• ")
		case "ol":
			para("ListParagraph", l.text, "")
		case "hr":
			body.WriteString(`<w:p><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="6" w:space="1" w:color="999999"/></w:pBdr></w:pPr></w:p>`)
		case "tr":
			body.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblW w:w="0" w:type="auto"/></w:tblPr>`)
			first := true
			for ; i < len(lines) && lines[i].kind == "tr"; i++ {
				body.WriteString("<w:tr>")
				for _, c := range lines[i].cells {
					body.WriteString("<w:tc><w:p>")
					if first {
						body.WriteString("<w:r><w:rPr><w:b/></w:rPr>")
					} else {
						body.WriteString("<w:r>")
					}
					fmt.Fprintf(&body, `<w:t xml:space="preserve">%s</w:t></w:r></w:p></w:tc>`, xmlText(strings.ReplaceAll(c, "**", "")))
				}
				body.WriteString("</w:tr>")
				first = false
			}
			i--
			body.WriteString("</w:tbl><w:p/>")
		default:
			para("", l.text, "")
		}
	}
	const ns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document ` + ns + `><w:body>` + body.String() +
		`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134" w:header="709" w:footer="709" w:gutter="0"/></w:sectPr></w:body></w:document>`
	style := func(id, name string, size int) string {
		return fmt.Sprintf(`<w:style w:type="paragraph" w:styleId="%s"><w:name w:val="%s"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:spacing w:before="240" w:after="120"/></w:pPr><w:rPr><w:b/><w:sz w:val="%d"/></w:rPr></w:style>`, id, name, size)
	}
	styles := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:styles ` + ns + `>` +
		`<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:cs="Calibri"/><w:sz w:val="22"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="276" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>` +
		`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>` +
		style("Heading1", "heading 1", 36) + style("Heading2", "heading 2", 30) + style("Heading3", "heading 3", 26) +
		`<w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/><w:basedOn w:val="Normal"/><w:pPr><w:ind w:left="360"/></w:pPr></w:style>` +
		`<w:style w:type="table" w:styleId="TableGrid"><w:name w:val="Table Grid"/><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="4" w:color="999999"/><w:left w:val="single" w:sz="4" w:color="999999"/><w:bottom w:val="single" w:sz="4" w:color="999999"/><w:right w:val="single" w:sz="4" w:color="999999"/><w:insideH w:val="single" w:sz="4" w:color="999999"/><w:insideV w:val="single" w:sz="4" w:color="999999"/></w:tblBorders></w:tblPr></w:style>` +
		`</w:styles>`
	return writeZip([][2]string{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/_rels/document.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`},
		{"word/document.xml", doc},
		{"word/styles.xml", styles},
	})
}

// ---------------------------------------------------------------- XLSX

func colName(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

var reNumber = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

func renderXLSX(csvText string) ([]byte, error) {
	r := csv.NewReader(strings.NewReader(csvText))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: not CSV: %v", ErrDocument, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: no rows", ErrDocument)
	}
	var sheet strings.Builder
	for ri, row := range rows {
		fmt.Fprintf(&sheet, `<row r="%d">`, ri+1)
		for ci, v := range row {
			ref := colName(ci) + strconv.Itoa(ri+1)
			v = strings.TrimSpace(v)
			style := ""
			if ri == 0 {
				style = ` s="1"`
			}
			if ri > 0 && reNumber.MatchString(v) && len(v) < 16 {
				fmt.Fprintf(&sheet, `<c r="%s"><v>%s</v></c>`, ref, v)
			} else if strings.HasPrefix(v, "=") {
				// Formulas are never written: a cell's text is stored as text.
				fmt.Fprintf(&sheet, `<c r="%s" t="inlineStr"%s><is><t xml:space="preserve">%s</t></is></c>`, ref, style, xmlText("'"+v))
			} else {
				fmt.Fprintf(&sheet, `<c r="%s" t="inlineStr"%s><is><t xml:space="preserve">%s</t></is></c>`, ref, style, xmlText(v))
			}
		}
		sheet.WriteString("</row>")
	}
	const ns = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"`
	return writeZip([][2]string{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook ` + ns + ` xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/styles.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><styleSheet ` + ns + `><fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf/></cellStyleXfs><cellXfs count="2"><xf/><xf fontId="1" applyFont="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`},
		{"xl/worksheets/sheet1.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet ` + ns + `><sheetData>` + sheet.String() + `</sheetData></worksheet>`},
	})
}

// ---------------------------------------------------------------- PDF

// winAnsi maps text to the PDF standard fonts' WinAnsi encoding; characters
// outside it become "?".
func winAnsi(s string) []byte {
	special := map[rune]byte{'€': 0x80, '‚': 0x82, '„': 0x84, '…': 0x85, '•': 0x95, '–': 0x96, '—': 0x97, '‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94, '™': 0x99}
	var b []byte
	for _, r := range s {
		switch {
		case r < 0x80 && r >= 0x20:
			b = append(b, byte(r))
		case r >= 0xA0 && r <= 0xFF:
			b = append(b, byte(r))
		default:
			if c, ok := special[r]; ok {
				b = append(b, c)
			} else {
				b = append(b, '?')
			}
		}
	}
	return b
}

func pdfString(b []byte) string {
	var s strings.Builder
	s.WriteByte('(')
	for _, c := range b {
		switch c {
		case '(', ')', '\\':
			s.WriteByte('\\')
			s.WriteByte(c)
		default:
			s.WriteByte(c)
		}
	}
	s.WriteByte(')')
	return s.String()
}

func wrap(text string, width int) []string {
	words := strings.Fields(text)
	var lines []string
	cur := ""
	for _, w := range words {
		if cur != "" && utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) > width {
			lines = append(lines, cur)
			cur = w
		} else if cur == "" {
			cur = w
		} else {
			cur += " " + w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func renderPDF(md string) ([]byte, error) {
	type line struct {
		font string
		size float64
		text string
		gap  float64
	}
	var lines []line
	for _, l := range parseMarkdown(md) {
		text := strings.ReplaceAll(l.text, "**", "")
		switch l.kind {
		case "h1":
			for _, w := range wrap(text, 48) {
				lines = append(lines, line{"F2", 18, w, 10})
			}
		case "h2":
			for _, w := range wrap(text, 60) {
				lines = append(lines, line{"F2", 14, w, 8})
			}
		case "h3":
			for _, w := range wrap(text, 70) {
				lines = append(lines, line{"F2", 12, w, 6})
			}
		case "li":
			for i, w := range wrap(text, 86) {
				p := "   "
				if i == 0 {
					p = "•  "
				}
				lines = append(lines, line{"F1", 11, p + w, 2})
			}
		case "tr":
			lines = append(lines, line{"F1", 10, strings.Join(l.cells, "  |  "), 2})
		case "hr":
			lines = append(lines, line{"F1", 11, strings.Repeat("_", 60), 4})
		default:
			for _, w := range wrap(text, 90) {
				lines = append(lines, line{"F1", 11, w, 2})
			}
		}
	}
	if len(lines) == 0 {
		lines = append(lines, line{"F1", 11, "", 0})
	}
	// Lay out on A4 pages (595 x 842 pt), 56 pt margins.
	var pages []string
	var cur strings.Builder
	y := 786.0
	for _, l := range lines {
		need := l.size*1.35 + l.gap
		if y-need < 56 && cur.Len() > 0 {
			pages = append(pages, cur.String())
			cur.Reset()
			y = 786
		}
		y -= need
		fmt.Fprintf(&cur, "BT /%s %.0f Tf 56 %.1f Td %s Tj ET\n", l.font, l.size, y, pdfString(winAnsi(l.text)))
	}
	pages = append(pages, cur.String())
	var buf bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	n := len(pages)
	// 1 catalog, 2 pages, 3 font F1, 4 font F2, then page/content pairs.
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, n)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 5+2*i)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	for i, p := range pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>", 6+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(p), p))
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------- reading

// ExtractText returns the readable text of a document's bytes.
func ExtractText(p string, data []byte) (string, error) {
	var s string
	var err error
	switch strings.ToLower(path.Ext(p)) {
	case ".docx":
		s, err = zipText(data, func(n string) bool { return n == "word/document.xml" }, "p")
	case ".pptx":
		s, err = zipText(data, func(n string) bool { return strings.HasPrefix(n, "ppt/slides/slide") && strings.HasSuffix(n, ".xml") }, "p")
	case ".xlsx":
		s, err = xlsxText(data)
	case ".pdf":
		s, err = pdfText(data)
	default:
		return "", fmt.Errorf("%w: cannot read %s", ErrDocument, path.Ext(p))
	}
	if err != nil {
		return "", err
	}
	if r := []rune(s); len(r) > maxDocText {
		s = string(r[:maxDocText]) + "\n[… text truncated]"
	}
	return s, nil
}

func openZip(data []byte) (*zip.Reader, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: not a valid Office file", ErrDocument)
	}
	return z, nil
}

func readEntry(f *zip.File, total *int64) ([]byte, error) {
	if f.UncompressedSize64 > maxDocEntry {
		return nil, fmt.Errorf("%w: %s is too large", ErrDocument, f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDocument, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxDocEntry+1))
	if err != nil || len(b) > maxDocEntry {
		return nil, fmt.Errorf("%w: %s unreadable or too large", ErrDocument, f.Name)
	}
	*total += int64(len(b))
	if *total > maxDocTotal {
		return nil, fmt.Errorf("%w: document too large", ErrDocument)
	}
	return b, nil
}

// xmlRuns collects the text of <t> elements, starting a new line at each
// paragraph element (local name para).
func xmlRuns(b []byte, para string) string {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	var out strings.Builder
	inT := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inT = true
			}
			if t.Name.Local == "tab" {
				out.WriteByte('\t')
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inT = false
			}
			if t.Name.Local == para || t.Name.Local == "tr" {
				out.WriteByte('\n')
			}
			if t.Name.Local == "tc" {
				out.WriteString(" | ")
			}
		case xml.CharData:
			if inT {
				out.Write(t)
			}
		}
	}
	return strings.TrimSpace(out.String())
}

func zipText(data []byte, want func(string) bool, para string) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	var names []string
	files := map[string]*zip.File{}
	for _, f := range z.File {
		if want(f.Name) {
			names = append(names, f.Name)
			files[f.Name] = f
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("%w: no text part", ErrDocument)
	}
	sort.Slice(names, func(i, j int) bool { return natLess(names[i], names[j]) })
	var total int64
	var out []string
	for i, n := range names {
		b, err := readEntry(files[n], &total)
		if err != nil {
			return "", err
		}
		t := xmlRuns(b, para)
		if strings.HasPrefix(n, "ppt/slides/") {
			t = fmt.Sprintf("[Slide %d]\n%s", i+1, t)
		}
		out = append(out, t)
	}
	return strings.Join(out, "\n\n"), nil
}

var reDigits = regexp.MustCompile(`\d+`)

// natLess orders slide2 before slide10.
func natLess(a, b string) bool {
	na, _ := strconv.Atoi(reDigits.FindString(a))
	nb, _ := strconv.Atoi(reDigits.FindString(b))
	if na != nb {
		return na < nb
	}
	return a < b
}

func xlsxText(data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	var total int64
	var shared []string
	var sheets []*zip.File
	for _, f := range z.File {
		switch {
		case f.Name == "xl/sharedStrings.xml":
			b, err := readEntry(f, &total)
			if err != nil {
				return "", err
			}
			var sst struct {
				SI []struct {
					T string `xml:"t"`
					R []struct {
						T string `xml:"t"`
					} `xml:"r"`
				} `xml:"si"`
			}
			xml.Unmarshal(b, &sst)
			for _, si := range sst.SI {
				s := si.T
				for _, r := range si.R {
					s += r.T
				}
				shared = append(shared, s)
			}
		case strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml"):
			sheets = append(sheets, f)
		}
	}
	sort.Slice(sheets, func(i, j int) bool { return natLess(sheets[i].Name, sheets[j].Name) })
	var out strings.Builder
	for si, f := range sheets {
		b, err := readEntry(f, &total)
		if err != nil {
			return "", err
		}
		var ws struct {
			Rows []struct {
				Cells []struct {
					T  string `xml:"t,attr"`
					V  string `xml:"v"`
					IS struct {
						T string `xml:"t"`
					} `xml:"is"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		xml.Unmarshal(b, &ws)
		fmt.Fprintf(&out, "[Sheet %d]\n", si+1)
		for _, r := range ws.Rows {
			var cells []string
			for _, c := range r.Cells {
				v := c.V
				switch c.T {
				case "s":
					if i, err := strconv.Atoi(c.V); err == nil && i >= 0 && i < len(shared) {
						v = shared[i]
					}
				case "inlineStr":
					v = c.IS.T
				}
				cells = append(cells, v)
			}
			out.WriteString(strings.Join(cells, "\t") + "\n")
		}
	}
	return strings.TrimSpace(out.String()), nil
}

var (
	reStream   = regexp.MustCompile(`(?s)<<(.*?)>>\s*stream\r?\n`)
	reTextOp   = regexp.MustCompile(`(?s)\((?:\\.|[^\\)])*\)\s*(?:Tj|'|")|\[(?:[^\]]*)\]\s*TJ|T\*|Td|TD|ET`)
	reLiteral  = regexp.MustCompile(`\((?:\\.|[^\\)])*\)`)
	reHexInTJ  = regexp.MustCompile(`<([0-9A-Fa-f\s]+)>`)
	maxStreams = 2000
)

// pdfText reads the text drawn by simple PDFs (literal strings in content
// streams, Flate-compressed or not). Scanned pages and fonts with custom
// encodings give little or no text; the caller says so.
func pdfText(data []byte) (string, error) {
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		return "", fmt.Errorf("%w: not a PDF", ErrDocument)
	}
	var out strings.Builder
	var total int64
	rest := data
	for n := 0; n < maxStreams; n++ {
		loc := reStream.FindSubmatchIndex(rest)
		if loc == nil {
			break
		}
		dict := string(rest[loc[2]:loc[3]])
		start := loc[1]
		end := bytes.Index(rest[start:], []byte("endstream"))
		if end < 0 {
			break
		}
		raw := rest[start : start+end]
		rest = rest[start+end:]
		if strings.Contains(dict, "/Subtype /Image") || strings.Contains(dict, "/Subtype/Image") {
			continue
		}
		content := raw
		if strings.Contains(dict, "FlateDecode") {
			zr, err := zlib.NewReader(bytes.NewReader(raw))
			if err != nil {
				continue
			}
			content, err = io.ReadAll(io.LimitReader(zr, maxDocEntry))
			zr.Close()
			if err != nil && len(content) == 0 {
				continue
			}
		} else if strings.Contains(dict, "/Filter") {
			continue // other encodings are not read
		}
		total += int64(len(content))
		if total > maxDocTotal {
			break
		}
		if !bytes.Contains(content, []byte("BT")) {
			continue
		}
		for _, op := range reTextOp.FindAll(content, -1) {
			s := string(op)
			switch {
			case s == "ET" || s == "T*" || s == "Td" || s == "TD":
				out.WriteByte('\n')
			case strings.HasSuffix(s, "TJ"):
				for _, lit := range reLiteral.FindAllString(s, -1) {
					out.WriteString(pdfUnescape(lit))
				}
				for _, h := range reHexInTJ.FindAllStringSubmatch(s, -1) {
					out.WriteString(hexText(h[1]))
				}
			default:
				if lit := reLiteral.FindString(s); lit != "" {
					out.WriteString(pdfUnescape(lit))
				}
			}
		}
	}
	text := regexp.MustCompile(`\n{3,}`).ReplaceAllString(strings.ReplaceAll(out.String(), "\r", ""), "\n\n")
	return strings.TrimSpace(text), nil
}

func pdfUnescape(lit string) string {
	s := lit[1 : len(lit)-1]
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b = append(b, c)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b = append(b, '\n')
		case 'r':
		case 't':
			b = append(b, '\t')
		case 'b', 'f':
		case '0', '1', '2', '3', '4', '5', '6', '7':
			j := i
			for j < len(s) && j < i+3 && s[j] >= '0' && s[j] <= '7' {
				j++
			}
			v, _ := strconv.ParseUint(s[i:j], 8, 8)
			b = append(b, byte(v))
			i = j - 1
		default:
			b = append(b, s[i])
		}
	}
	// Literal strings are mostly WinAnsi/Latin-1: map bytes to runes.
	r := make([]rune, 0, len(b))
	for _, c := range b {
		r = append(r, rune(c))
	}
	return string(r)
}

func hexText(h string) string {
	h = strings.Join(strings.Fields(h), "")
	if len(h)%2 == 1 {
		h += "0"
	}
	var b []byte
	for i := 0; i+1 < len(h); i += 2 {
		v, err := strconv.ParseUint(h[i:i+2], 16, 8)
		if err == nil {
			b = append(b, byte(v))
		}
	}
	// Two-byte strings (Identity-H) cannot be mapped without the font's CMap.
	for _, c := range b {
		if c == 0 {
			return ""
		}
	}
	return string(b)
}
