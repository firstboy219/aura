# ADR-0064: Office documents — write Word, Excel and PDF; read Word, Excel, PowerPoint and PDF (JARVIS parity, wave 3)

Status: accepted (owner instruction of 2026-11-11, wave 3 of the plan in ADR-0062)

## Context
JARVIS creates Word/Excel/PDF files and reads the text of attachments. AURA's workspace tools handled UTF-8 text
only, and the file endpoint served everything as text.

## Decision
1. **Writing.** `workspace.write` to a `.docx` or `.pdf` path takes Markdown (headings, paragraphs, bullets, pipe
   tables, bold); to an `.xlsx` path it takes CSV. The Core renders the file with the Go standard library from fixed
   templates (zip + Office Open XML; a plain PDF with the standard Helvetica fonts, WinAnsi text). Rendering is
   deterministic (fixed archive timestamps), so a retried write intent is recognised and skipped. Spreadsheet cells
   that start with "=" are stored as text with a leading apostrophe: no formula is ever written.
2. **Reading.** `workspace.read` of `.docx`, `.xlsx`, `.pptx` or `.pdf` returns the document's text: Office parts are
   read as XML with per-part (8 MiB) and per-document (24 MiB) limits against archive bombs; PDF text is taken from
   literal strings in content streams (Flate or uncompressed). Scanned PDFs and fonts with custom encodings give
   little text, and the observation says so.
3. **Downloading.** `GET /api/v1/tasks/{id}/files/{path}` serves documents and pictures as attachments with their
   content type, `nosniff` and the sandbox CSP; other files stay `text/plain` inline. The Android app opens a
   reply's file in another app through a `FileProvider` share of a cache copy (only the document being opened).
4. Verified on 2026-11-11 with python-docx, openpyxl and pypdf in a throwaway container: the three generated formats
   open and contain the expected text, tables and numbers.

## Alternatives
- A document library dependency (e.g. a Go DOCX/PDF package): unnecessary for the formats needed, and each would
  need a licence and supply-chain review (AGENTS §5).
- Converting with LibreOffice in a container: heavy (hundreds of MB) for little gain.

## Consequences
- PDFs use the standard fonts: characters outside Latin-1 (e.g. Arabic) become "?" in generated PDFs; Word and
  Excel files keep full Unicode.
- Uploading binary attachments from the phone is not part of this change (attachments are text today).
