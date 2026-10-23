# Agent Handoff Note: PDF-to-EPUB Engine Implementation & Fixes

**Project:** `rebind`  
**Date:** October 20, 2026  
**Status:** All core implementation complete, bug fixes applied, all unit & integration tests passing (`go test ./...`), binary builds cleanly (`make build`).

---

## 1. Context of this Work

The `rebind` project originally converted Web URLs into clean EPUB files using Readability and a Bubble Tea TUI. The goal of this phase was to expand `rebind` to support **local PDF files and remote PDF URLs**, converting them into clean, reflowable, typographically polished EPUB books for **Apple Books and Kindle**.

### Key Design Goals
- **100% deterministic Go + Poppler CLI pipeline** (`pdfinfo`, `pdftohtml`, `pdftoppm`): No Python, Calibre, or LLM runtime dependencies.
- **Dedicated book profiles**:
  - `ProfileFiction`: Drop caps, scene breaks (`* * *`, `— — —`), dialogue line preservation, hyphen-aware fusion via document lexicon.
  - `ProfileTechnical`: Monospace preservation, ASCII/Unicode box-drawing diagrams (`┌─┐`), inline code tagging, simple tables to HTML `<table>`, complex tables & vector diagrams cropped to high-DPI PNGs.
- **Strict XML validity**: Required by Apple Books and Kindle e-readers.
- **100% backward compatibility**: The existing Web URL conversion flow must remain untouched and fully operational.

### User Feedback & Reported Bugs
During testing with a real book PDF (Harry Potter / fiction novel), the user observed:
- **Raw `<a>` tags visible on screen**: e.g., `<a href="book.html#208">The House of Gaunt · 194 </a>` rendered as literal text in Apple Books.
- **Raw `<i>` tags visible on screen**: Italic text showing literal `<i>...</i>` tags on the page.

---

## 2. What Has Been Done So Far

### A. Core Architecture Implemented
1. **[`internal/converter/converter.go`](file:///Users/sunday/Dev/Personal/Projects/web2reader/internal/converter/converter.go)**:
   - Added `InputKind` detection (`InputWebURL`, `InputLocalPDF`, `InputRemotePDF`) in `DetectInputKind`. Handles unquoted paths, single/double quotes, backslash-escaped spaces (Finder drag-and-drop), and `file://` URLs.
   - Added `BookProfile` (`ProfileAuto`, `ProfileFiction`, `ProfileTechnical`) and `ConvertOptions`.
   - Updated `epubStylesheet` with styles for `.scene-break`, `.dropcap`, `.table-figure`, and `.table-wrap`.
   - Added `ConvertWithOptions` as the primary entry point while keeping `Convert` backward-compatible.

2. **[`internal/converter/pdf_poppler.go`](file:///Users/sunday/Dev/Personal/Projects/web2reader/internal/converter/pdf_poppler.go)**:
   - Binary resolution with automatic lookup in `PATH` and macOS `/opt/homebrew/bin/` fallback.
   - `runPDFInfo`: Extracts page count, title, author, subject.
   - `runPDFToHTMLXML`: Executes `pdftohtml -xml -nodrm -enc UTF-8` to extract structured geometric XML.
   - `parsePopplerXML`: Unmarshals Poppler's XML (`<page>`, `<fontspec>`, `<text>`, `<image>`, `<outline>`).
   - `classifyFont`: Strips random font subset prefixes (`FAAAAA+Helvetica` → `Helvetica`) and classifies monospace, bold, italic.
   - `CropPageRegionPNG`: High-DPI (300 DPI) bounding box cropping via `pdftoppm -png -r 300 -f <page> -l <page> -x <x> -y <y> -W <w> -H <h>`.

3. **[`internal/converter/pdf_layout.go`](file:///Users/sunday/Dev/Personal/Projects/web2reader/internal/converter/pdf_layout.go)**:
   - Unicode typographic ligature expansion (`\uFB00`–`\uFB06` → `ff`, `fi`, `fl`, `ffi`, `ffl`, `st`).
   - `BuildVisualLines`: Baseline clustering (`|topA - topB| <= 3.5px`), font dominance detection, and intra-line column gap detection (`TextCell`).
   - `StripRunningNoise`: Statistical cross-page header, footer, print timestamp, and page number removal using Y-band template clustering.
   - `SplitPageColumns`: Center gutter detection for 2-column layouts; restores proper natural reading order (Spanning Top → Left Column → Right Column → Spanning Bottom).

4. **[`internal/converter/pdf_statemachine.go`](file:///Users/sunday/Dev/Personal/Projects/web2reader/internal/converter/pdf_statemachine.go)**:
   - `ResolveBookProfile`: Auto-detects Technical vs Fiction based on monospace font density, code tokens, and technical captions.
   - `DocumentLexicon`: Counts split vs unsplit word frequencies across the PDF to accurately determine whether a hyphenated line break should be merged (`reflow-able` → `reflowable`) or preserved (`well-known` → `well-known`).
   - Block classification: Heading 1/2/3, Scene Breaks (`<hr class="scene-break"/>`), Paragraphs, Code blocks (`<pre><code>`), Tables (`<table>` or high-DPI cropped image), Figures/Diagrams (`<figure><img .../><figcaption>`).
   - Drop cap extraction and dialogue line separation.

5. **[`internal/converter/pdf_converter.go`](file:///Users/sunday/Dev/Personal/Projects/web2reader/internal/converter/pdf_converter.go)**:
   - Multi-chapter splitting via PDF outline/bookmarks, or `<h1>` headers, or 20-page fallback chunks.
   - Raster image deduplication and cover image auto-detection on Page 1.
   - Safe strict XHTML fragment validation using `encoding/xml`.
   - Full packaging into EPUB format using `github.com/go-shiori/go-epub`.

6. **[`internal/tui/tui.go`](file:///Users/sunday/Dev/Personal/Projects/web2reader/internal/tui/tui.go)**:
   - Interactive profile cycling (`Auto` → `Fiction` → `Technical`) via `Tab` / `Shift+Tab`.
   - Dynamic step messages tuned for PDF conversion.
   - Extended result display (Page count, Chapter count, Profile used).

---

### B. Bug Fixes & Refinements Applied

#### 1. Root Cause of Raw `<a>` and `<i>` Tags Rendered on Screen
- **The Bug:** `parseInnerXML` in `pdf_layout.go` called `html.ParseFragment(reader, &html.Node{Type: html.ElementNode, Data: "div"})`. In `golang.org/x/net/html`, `ParseFragment` fails if the context node lacks `DataAtom` (returning error `html: inconsistent Node: DataAtom="", Data="div"`).
- **The Failure Cascading:** Because `err != nil`, execution fell back to:
  ```go
  clean := sanitizeXMLChars(innerXML)
  escapeXMLText(&buf, clean)
  return clean, buf.String(), ...
  ```
  This caused `escapeXMLText` to turn `<a href="...">` and `<i>` into `&lt;a href="..."&gt;` and `&lt;i&gt;`, which Apple Books and Kindle displayed as literal raw HTML characters on the screen! Additionally, `clean` contained raw `<...>` tags, corrupting `PlainText`.
- **The Fix:**
  - In `pdf_layout.go`, passed `contextNode := &html.Node{Type: html.ElementNode, DataAtom: atom.Body, Data: "body"}` to `html.ParseFragment`.
  - Added `stripHTMLTags` fallback to guarantee no raw HTML tags ever leak into `PlainText` even if an unparseable fragment occurs.
  - Expanded `parseInnerXML` to support:
    - `<b>`, `<strong>` → `<strong>`
    - `<i>`, `<em>` → `<em>`
    - `<sub>`, `<sup>`, `<code>`, `<tt>`, `<del>`, `<s>`, `<u>`
    - Nested combinations (e.g. `<a href="..."><i>text</i></a>`).

#### 2. Poppler Phantom Link Rewriting & Page Anchoring
- **The Bug:** Poppler generates links pointing to `book.html#<pageNum>`. In an EPUB, `book.html` does not exist (the document is partitioned into `chapter_001.xhtml`, `chapter_002.xhtml`, etc.), resulting in broken links or validation errors.
- **The Fix (`pdf_converter.go`):**
  - Inserted invisible page anchors `<div id="p<N>"></div>` for each page boundary within sections.
  - Added `rewritePopplerLinks`:
    - Cross-chapter links (`book.html#208`) are rewritten to `chapter_011.xhtml#p208`.
    - Same-chapter links are rewritten to `#p208`.
    - Unresolvable links to non-existent pages in phantom `.html` files are safely unwrapped into plain text with no broken anchor.
    - External links (`http://`, `https://`, `mailto:`) remain untouched.

#### 3. Rich Formatting Preservation
- Updated `pdf_statemachine.go` and `pdf_layout.go` so that headings (`<h1>`, `<h2>`, `<h3>`), figure captions, and list items preserve `RichXHTML` formatting (`<em>`, `<strong>`, `<code>`) rather than stripping or escaping it.
- Font-level italics (e.g., `TimesNewRoman-Italic` without explicit `<i>` tags) are automatically wrapped in `<em>` if the surrounding line is not italic.

#### 4. Tests Added
- Added `TestParseInnerXMLAndLinkRewriting` to [`pdf_converter_test.go`](file:///Users/sunday/Dev/Personal/Projects/web2reader/internal/converter/pdf_converter_test.go#L520-L630):
  - Validates clean text extraction without tags for `PlainText`.
  - Validates `richXHTML` does not contain escaped raw tags (`&lt;a`, `&lt;i`).
  - Tests nested tags, unclosed tag auto-repair, and cross/same chapter link rewriting.
  - Verifies that all generated chapter files pass strict Go `encoding/xml` validation.

---

## 3. What is Left / Potential Improvements

1. **Footnote & Endnote Detection:**
   - Detect superscript numbers or brackets (e.g. `[1]`, `¹`) in paragraph bodies and link them to corresponding footnote definitions at the bottom of the page or chapter.
2. **Mathematical Formula Handling:**
   - PDF math formulas often consist of dozens of fragmented glyph runs and sub/superscripts that render poorly in reflowable HTML.
   - Detecting clustered math fonts (e.g., `CMSY`, `MathJax`, `CambriaMath`) and automatically routing that bounding box to `CropPageRegionPNG` would yield crisp vector diagrams of complex formulas.
3. **Multi-Page Table Support:**
   - Tables that span across multiple consecutive pages currently create separate `<table>` blocks or separate images for each page. Joining contiguous table structures across page boundaries would produce cleaner documents.
4. **Memory Optimization for Very Large Documents:**
   - Currently, `BuildVisualLines` and the state machine process all pages in memory at once. For massive PDFs (> 1,500 pages), stream-processing pages or buffering temporary blocks to disk could reduce peak memory.
5. **Headless / CLI Mode Flags:**
   - `main.go` currently launches Bubble Tea TUI. Adding optional CLI flags (e.g. `rebind --profile fiction --output ./out book.pdf`) would allow automated scripting or batch conversion.

---

## 4. Things to Bear in Mind for the Next Agent

### A. Environment Prerequisites
- Poppler must be installed on the host system:
  - macOS: `brew install poppler`
  - Linux: `apt-get install poppler-utils`
- The binaries checked are `pdfinfo`, `pdftohtml`, and `pdftoppm`. If `pdftohtml` is missing, tests skipping logic triggers for integration tests, but runtime conversions will fail.

### B. Architectural Invariants
1. **Never use `escapeXMLText` on strings that already contain XML tags:**
   - `PlainText` is for heuristics, regex matches, and lexicon hyphenation.
   - `RichXHTML` is already valid XML markup. Passing `RichXHTML` into `escapeXMLText` will escape the tags into `&lt;...&gt;` and cause the raw tag bug to regress!
2. **Strict XHTML is non-negotiable for e-readers:**
   - Apple Books and Kindle will fail to open or flag EPUB files with XML syntax errors (e.g., unclosed tags, unescaped ampersands).
   - Always run `validateXHTMLFragment` (or `xml.NewDecoder`) on any generated markup.
3. **`html.ParseFragment` Context Node:**
   - When calling `html.ParseFragment(reader, contextNode)`, `contextNode` **must** have both `Type: html.ElementNode` and a valid `DataAtom` (e.g. `atom.Body` or `atom.Div`). Omitting `DataAtom` causes parsing to silently fail.
4. **Backward Compatibility:**
   - Existing URL-to-EPUB tests in `converter_test.go` must pass without modifications. Any changes to `xhtml.go` or `converter.go` must ensure standard HTML scraping remains intact.

---

## 5. Quick Verification Commands

```bash
# Run all unit and integration tests
go test -v ./...

# Run the specific PDF converter regression suite
go test -v ./internal/converter -run TestParseInnerXMLAndLinkRewriting

# Build the binary
make build

# Run the binary with a sample PDF
./bin/rebind path/to/sample.pdf
```
