# PDF-to-EPUB Reflow & Conversion Engine — Implementation Specification

## 1. Goal & Scope

Expand `rebind` so users can convert **local PDF files and remote PDF URLs** into clean, reflowable, typographically polished EPUB books for **Apple Books and Kindle**—specifically optimized for **Fiction novels** and **Technical books** (with code blocks, vector/ASCII diagrams, and tables) using a fast, **100% deterministic Go + Poppler CLI pipeline** (no LLM dependencies).

All existing Web URL → EPUB functionality and tests ([`internal/converter/converter_test.go`](file:///Users/sunday/Dev/Personal/Projects/web2reader/internal/converter/converter_test.go)) must remain 100% backward-compatible.

---

## 2. Current Codebase Reference

- **[`main.go`](file:///Users/sunday/Dev/Personal/Projects/web2reader/main.go)**:
  - Resolves `~/Documents` via `converter.DefaultOutputDir()`, reads optional `os.Args[1]`, and launches Bubble Tea (`tui.NewModel(initialInput, outputDir)`).
- **[`internal/tui/tui.go`](file:///Users/sunday/Dev/Personal/Projects/web2reader/internal/tui/tui.go)**:
  - Bubble Tea state machine (`stateInput`, `stateConverting`, `stateDone`, `stateError`).
  - Streams `converter.ProgressEvent` over a channel (`progressCh`) and offers `o` (Open in Apple Books via `open -a Books`), `r` (Reveal in Finder via `open -R`), `enter`/`n` (convert another), and `q`/`esc` (quit).
- **[`internal/converter/converter.go`](file:///Users/sunday/Dev/Personal/Projects/web2reader/internal/converter/converter.go)**:
  - Defines `Step` (`StepFetching`, `StepExtracting`, `StepLocalizingAssets`, `StepPackaging`, `StepComplete`), `ProgressEvent`, `Result`, `epubStylesheet`, and `Convert(ctx, rawURL, outputDir, onProgress)`.
- **[`internal/converter/xhtml.go`](file:///Users/sunday/Dev/Personal/Projects/web2reader/internal/converter/xhtml.go)**:
  - Provides `SanitizeToXHTML`, `sanitizeXMLChars`, `escapeXMLText`, `escapeXMLAttr`, and `validateXHTMLFragment` (which parses generated fragments with Go's standard `encoding/xml` decoder so Apple Books and Kindle never fail on malformed XHTML).

---

## 3. High-Level Architecture

```
Raw Input String (URL or Local Path / Drag-and-Drop .pdf)
  │
  ├─► InputSourceWebURL ──► Existing Readability HTML Pipeline (unchanged)
  │
  └─► InputSourceLocalPDF / InputSourceRemotePDF
        │
        ▼
  [Step 1: StepFetching — Acquire & Inspect PDF]
    • Unescape shell/Finder drag-and-drop paths, expand `~`, or download remote `.pdf`
    • Verify Poppler (`pdftohtml`, `pdftoppm`, `pdfinfo`) on PATH (with `/opt/homebrew/bin` fallback)
    • Run `pdfinfo` + `pdftohtml -xml -nodrm -enc UTF-8 <pdf> <tmpDir>/book.xml`
    • Parse `<page>`, `<fontspec>`, `<text>`, `<image>`, and `<outline>` into Go structs
        │
        ▼
  [Step 2: StepExtracting — Spatial Normalization & Layout Reconstruction]
    • Expand Unicode ligatures (ﬀ, ﬁ, ﬂ, ﬃ, ﬄ) & strip invalid XML runes
    • Strip font subset prefixes (`AAAAAA+Helvetica` → `Helvetica`) & classify fonts (Monospace, Bold, Italic, Math)
    • Coalesce horizontal `<text>` runs on the same baseline (`|topA - topB| <= 3px`) into `VisualLine`s
    • Strip running headers, footers, print timestamps, and page numbers via cross-page Y-band clustering
    • Detect 1-column vs 2-column page geometry via X-projection gutter histogram
    • Auto-detect `BookProfile` (`ProfileFiction` vs `ProfileTechnical`) if set to `ProfileAuto`
        │
        ▼
  [Step 3: StepLocalizingAssets — Deterministic Parser State Machine & Region Rendering]
    • StateBodyText  ──► Paragraph fusion, dialogue separation, scene breaks, drop caps, lexicon de-hyphenation
    • StateCodeBlock ──► Monospace / indented code blocks & ASCII box-drawing diagrams → `<pre><code>`
    • StateTable     ──► ≤3 col simple grid → `<table>`; >3 col or dense grid → 300-DPI PNG via `pdftoppm -singlefile`
    • StateFigure    ──► Raster `<image>` + vector diagram gap cropping (`pdftoppm`) paired with `<figcaption>`
        │
        ▼
  [Step 4: StepPackaging — Multi-Chapter Splitting, Strict XHTML Validation & EPUB Packaging]
    • Split into chapters (`chapter_001.xhtml`, ...) using PDF `<outline>` bookmarks or `<h1>`/`<h2>` headings
    • Validate every chapter string with `validateXHTMLFragment`
    • Attach cover image (if page 1 is a full-bleed cover) and enhanced `epubStylesheet` via `go-epub`
```

---

## 4. Public Types & API Contracts (`internal/converter/converter.go`)

Keep `Convert` with its exact signature so existing callers and tests compile untouched, and add `ConvertWithOptions`:

```go
// InputKind identifies whether the user entered a web article URL, a local PDF path, or a remote PDF URL.
type InputKind int

const (
	InputWebURL InputKind = iota
	InputLocalPDF
	InputRemotePDF
)

// BookProfile controls layout heuristics when converting a PDF.
type BookProfile string

const (
	ProfileAuto      BookProfile = "auto"
	ProfileFiction   BookProfile = "fiction"
	ProfileTechnical BookProfile = "technical"
)

// ConvertOptions configures a conversion run.
type ConvertOptions struct {
	OutputDir string
	Profile   BookProfile // Default: ProfileAuto (only used for PDF inputs)
}

// Extend Result with optional PDF fields (backward-compatible):
type Result struct {
	Title       string
	Author      string
	SiteName    string
	Excerpt     string
	SourceURL   string
	OutputPath  string
	ImagesTotal int
	ImagesSaved int
	PagesTotal  int         // Populated for PDF conversions
	Chapters    int         // Number of EPUB chapter sections generated
	ProfileUsed BookProfile // Resolved profile (ProfileFiction or ProfileTechnical) for PDFs
	Duration    time.Duration
}

// Convert preserves backward compatibility and delegates to ConvertWithOptions.
func Convert(ctx context.Context, rawInput string, outputDir string, onProgress func(ProgressEvent)) (*Result, error) {
	return ConvertWithOptions(ctx, rawInput, ConvertOptions{
		OutputDir: outputDir,
		Profile:   ProfileAuto,
	}, onProgress)
}
```

### Input Detection Rules (`DetectInputKind(raw string) (InputKind, string, error)`)

1. **Trim & Unquote Drag-and-Drop Paths**:
   - Trim whitespace. If wrapped in matching `'...'` or `"..."`, strip quotes.
   - If the string does **not** start with `http://` or `https://`:
     - Handle `file://` URLs via `url.Parse` + `url.PathUnescape`.
     - Unescape backslash-escaped spaces (`\ ` → ` `) produced when dragging a file from macOS Finder into a terminal.
     - Expand leading `~/` using `os.UserHomeDir()`.
     - If the resolved path ends with `.pdf` (case-insensitive) or exists on disk as a regular file whose first 5 bytes are `%PDF-`, return `(InputLocalPDF, absPath, nil)`.
     - If the string looks like a local filesystem path (starts with `/`, `./`, `../`, `~/`, or ends with `.pdf`) and the file does not exist, return a clear error: `fmt.Errorf("PDF file not found: %s", cleanedPath)`.
2. **Remote HTTP/HTTPS URLs**:
   - Normalize via `NormalizeURL`.
   - If `strings.HasSuffix(strings.ToLower(parsedURL.Path), ".pdf")`, return `(InputRemotePDF, normalizedURL, nil)`.
   - Also in `ConvertWithOptions`, if `InputWebURL` is fetched via `fetchHTML` and the HTTP response `Content-Type` starts with `application/pdf` OR the body starts with `%PDF-`, automatically route the downloaded bytes to the PDF pipeline (`InputRemotePDF`).

---

## 5. Poppler CLI Integration (`internal/converter/pdf_poppler.go`)

### 5.1 Binary Resolution (`findPopplerBin(name string) (string, error)`)

- Check `exec.LookPath(name)`.
- Because macOS GUI/TUI environments occasionally have minimal `PATH`, also check `/opt/homebrew/bin/<name>` and `/usr/local/bin/<name>` if `LookPath` fails.
- If `pdftohtml` or `pdftoppm` is missing, return:
  `fmt.Errorf("Poppler tools (%s) not found; install with: brew install poppler", name)`

### 5.2 Running `pdfinfo` and `pdftohtml -xml`

1. **Metadata (`pdfinfo <pdfPath>`)**:
   - Parse key-value lines (`Title:`, `Author:`, `Pages:`).
   - **Important Edge Case**: Browser-printed PDFs often set `Title: about:blank`, `Untitled`, or a raw filename (`microsoft word - ...`). Treat titles matching `(?i)^(about:blank|untitled|none|document|\.pdf)$` as empty so the converter falls back to the first detected `<h1>` title on Page 1 or the cleaned PDF filename!
2. **Spatial XML + Raster Image Extraction**:
   - Run in `tmpDir`:
     ```bash
     pdftohtml -xml -nodrm -enc UTF-8 <pdfPath> <tmpDir>/book.xml
     ```
   - Note: Running **without** `-i` instructs Poppler to extract embedded raster images into `<tmpDir>/book*.png` or `<tmpDir>/book*.jpg` and reference them in `<image top="..." left="..." width="..." height="..." src="..."/>` elements inside `book.xml`.

### 5.3 Poppler `book.xml` Schema & Parsing Details

Actual `pdftohtml -xml` output looks like:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE pdf2xml SYSTEM "pdf2xml.dtd">
<pdf2xml producer="poppler" version="25.12.0">
<page number="1" position="absolute" top="0" left="0" height="1188" width="918">
  <fontspec id="0" size="27" family="AAAAAA+Helvetica" color="#000000"/>
  <fontspec id="5" size="13" family="FAAAAA+Menlo" color="#000000"/>
  <image top="140" left="120" width="400" height="260" src="book-1_1.png"/>
  <text top="64" left="64" width="470" height="27" font="0"><b>PDF to EPUB Conversion</b></text>
  <text top="314" left="64" width="620" height="13" font="3">preserving physical line breaks (</text>
  <text top="312" left="684" width="32" height="15" font="5">&lt;br&gt;</text>
  <text top="314" left="716" width="126" height="13" font="3"> or hard line-wrapped</text>
</page>
<outline>
  <item page="1">Chapter 1: Introduction</item>
  <outline>
    <item page="3">1.1 Background</item>
  </outline>
</outline>
</pdf2xml>
```

**Go XML Structs**:

```go
type xmlDocument struct {
	XMLName xml.Name       `xml:"pdf2xml"`
	Pages   []xmlPage      `xml:"page"`
	Outline *xmlOutline    `xml:"outline"`
}

type xmlPage struct {
	Number int           `xml:"number,attr"`
	Top    float64       `xml:"top,attr"`
	Left   float64       `xml:"left,attr"`
	Width  float64       `xml:"width,attr"`
	Height float64       `xml:"height,attr"`
	Fonts  []xmlFontSpec `xml:"fontspec"`
	Images []xmlImage    `xml:"image"`
	Texts  []xmlText     `xml:"text"`
}

type xmlFontSpec struct {
	ID     string  `xml:"id,attr"`
	Size   float64 `xml:"size,attr"`
	Family string  `xml:"family,attr"`
	Color  string  `xml:"color,attr"`
}

type xmlImage struct {
	Top    float64 `xml:"top,attr"`
	Left   float64 `xml:"left,attr"`
	Width  float64 `xml:"width,attr"`
	Height float64 `xml:"height,attr"`
	Src    string  `xml:"src,attr"`
}

type xmlText struct {
	Top      float64 `xml:"top,attr"`
	Left     float64 `xml:"left,attr"`
	Width    float64 `xml:"width,attr"`
	Height   float64 `xml:"height,attr"`
	FontID   string  `xml:"font,attr"`
	InnerXML string  `xml:",innerxml"`
}
```

- **Font Normalization (`classifyFont(spec xmlFontSpec) FontInfo`)**:
  - Strip subset tag prefix matching `^[A-Z]{6}\+` (e.g., `FAAAAA+Menlo-Regular` → `Menlo-Regular`).
  - `IsMonospace`: match case-insensitive substring against `mono`, `courier`, `consolas`, `menlo`, `monaco`, `firacode`, `fira code`, `sourcecode`, `source code`, `inconsolata`, `jetbrains`, `dejavusansmono`, `liberationsansmono`, `lucideconsole`, ` typewriter`, `fixed`.
  - `IsBold`: family contains `bold`, `black`, `heavy`, `demi`, `semibold` OR `InnerXML` is wrapped in `<b>...</b>`.
  - `IsItalic`: family contains `italic`, `oblique`, `slanted` OR `InnerXML` is wrapped in `<i>...</i>`.
- **High-DPI Region Cropping (`CropPageRegionPNG`)**:
  - When rendering a complex table or a vector diagram on `page.Number` with bounding box `[left, top, width, height]` in `pdftohtml` coordinates (`page.Width x page.Height`, which Poppler generates at 108 DPI):
  - Convert to 300-DPI pixel coordinates using `scale := 300.0 / 108.0`:
    - `cropX := max(0, int(math.Floor((left - pad) * scale)))`
    - `cropY := max(0, int(math.Floor((top - pad) * scale)))`
    - `cropW := int(math.Ceil((width + 2*pad) * scale))`
    - `cropH := int(math.Ceil((height + 2*pad) * scale))`
  - Execute:
    ```bash
    pdftoppm -png -r 300 -f <pageNum> -l <pageNum> -singlefile \
      -x <cropX> -y <cropY> -W <cropW> -H <cropH> \
      <pdfPath> <outFilePrefix>
    ```
  - This produces `<outFilePrefix>.png` directly.

---

## 6. Spatial Layout Analysis (`internal/converter/pdf_layout.go`)

### 6.1 Unicode Ligature Expansion & Sanitization

Replace typographic presentation ligatures before word analysis so dictionary/lexicon checks and e-reader search work properly:
- `\uFB00` (`ﬀ`) → `ff`
- `\uFB01` (`ﬁ`) → `fi`
- `\uFB02` (`ﬂ`) → `fl`
- `\uFB03` (`ﬃ`) → `ffi`
- `\uFB04` (`ﬄ`) → `ffl`
- `\uFB05` (`ﬅ`) → `st`
- `\uFB06` (`ﬆ`) → `st`
- Also pass through `sanitizeXMLChars` from `xhtml.go`.

### 6.2 Horizontal Fragment Coalescing (`BuildVisualLines`)

In `pdftohtml -xml`, a single visual line with bold, italic, or inline `<code>` words is emitted as multiple `<text>` elements at the same vertical position (e.g. `top="314"` normal font and `top="312"` inline monospace font).

1. Sort all `<text>` runs on a page by `Top` ascending, then `Left` ascending.
2. Group `<text>` runs into the same row cluster if their vertical baselines overlap (`math.Abs((runA.Top + runA.Height) - (runB.Top + runB.Height)) <= max(3.5, 0.25*minHeight)` or `math.Abs(runA.Top - runB.Top) <= 3.5`).
3. Within each row cluster, sort runs by `Left` ascending and evaluate horizontal gap `gap = next.Left - (curr.Left + curr.Width)`:
   - Estimate average character width `charW = max(5.0, curr.Height * 0.52)`.
   - **Normal inline continuation (`gap <= 2.2 * charW`)**:
     - Merge into the current `VisualLine`.
     - If `gap > 0.22 * charW` and neither side already has boundary whitespace or opening/closing punctuation (`(`, `[`, `"`), insert a single space `" "`.
     - Preserve inline formatting in `VisualLine.RichXHTML`:
       - If a run uses a monospace font while the line's dominant font is proportional, wrap that run's escaped text in `<code>...</code>`.
       - Preserve `<b>` → `<strong>`, `<i>` → `<em>`, `<a href="...">` → `<a href="...">`.
   - **Wide column/table gap (`gap > 2.2 * charW`)**:
     - Record `CellStops []TextCell` on the `VisualLine` (storing each cell's `Left`, `Right`, `PlainText`, `RichXHTML`). If a `VisualLine` has `>= 2` cells separated by wide gaps, flag `line.HasMultipleColumns = true`.

### 6.3 Running Header, Footer & Page Artifact Stripping (`StripRunningNoise`)

Scan across all pages before paragraph assembly:
1. Define the **Header Zone** (`line.Top < 0.085 * page.Height`) and **Footer Zone** (`line.Top + line.Height > 0.915 * page.Height`).
2. Normalize each header/footer candidate line by replacing digits with `#` and trimming whitespace (e.g. `"Page 14 of 220"` → `"page # of #"`).
3. Drop any line in the Header or Footer Zone that:
   - Matches standalone page numbers or print metadata:
     - `^\d+$`, `^[ivxlcdmIVXLCDM]+$`, `(?i)^page\s+\d+`, `^\d+\s*(/|of)\s*\d+$`
     - Browser print artifacts: `^about:blank$`, `^\d{1,2}/\d{1,2}/\d{2,4},?\s+\d{1,2}:\d{2}`
   - Or whose normalized template appears in the same vertical zone (`±5px`) on **at least 3 pages** (or `>= 40%` of pages for short PDFs with 3–6 pages).

### 6.4 Multi-Column Gutter Detection (`SplitPageColumns`)

For each page, examine body-sized lines (excluding headings, headers/footers, and full-width figures):
- Check if there is a vertical corridor near the horizontal center (`x` in `[0.42*page.Width, 0.58*page.Width]`) that no normal body line crosses, while lines exist both to the left (`Right <= gutterX`) and to the right (`Left >= gutterX`) across `>= 40%` of the page's vertical span.
- If detected: partition the page elements into Left Column (top-to-bottom), then Right Column (top-to-bottom), keeping full-width spanning titles/figures in their vertical reading order.

---

## 7. Deterministic Parser State Machine (`internal/converter/pdf_statemachine.go`)

### 7.1 Auto-Profile Detection (`ResolveBookProfile`)

If `opts.Profile == ProfileAuto`:
- Scan all pages for:
  - Ratio of monospace `<text>` runs (`monoRunCount / totalRunCount`)
  - Count of technical captions (`^(Figure|Fig\.|Table|Listing|Algorithm)\s+\d+`)
  - Count of code-like lines or multi-column tables
- If `monoRatio >= 0.025` OR `techCaptionCount >= 2` OR `codeBlockLines >= 6`, resolve to `ProfileTechnical`; otherwise resolve to `ProfileFiction`.

### 7.2 Document-Wide De-Hyphenation Lexicon (`BuildDocumentLexicon`)

Before fusing lines, walk every word in the PDF that does **not** sit at a line break:
- Record `unsplitWords[strings.ToLower(word)]++` (e.g. `"reflowable": 4`, `"architecture": 12`).
- Record `hyphenatedWords[strings.ToLower(word)]++` (e.g. `"well-known": 3`, `"multi-column": 5`, `"coordinate-based": 2`).

When `Line N` ends with `word1-` and `Line N+1` begins with `word2`:
1. If `word2` starts with an uppercase letter (e.g. `non-European`), keep hyphen: `word1 + "-" + word2`.
2. Let `joined = strings.ToLower(word1 + word2)` and `hyph = strings.ToLower(word1 + "-" + word2)`.
3. If `hyphenatedWords[hyph] > unsplitWords[joined]`, keep hyphen (`word1 + "-" + word2`).
4. If `unsplitWords[joined] > 0`, strip hyphen (`word1 + word2`).
5. If `strings.ToLower(word1)` is in a known compound-prefix set (`"self"`, `"well"`, `"all"`, `"ex"`, `"non"`, `"cross"`, `"half"`, `"high"`, `"low"`, `"state"`, `"quasi"`, `"ultra"`, `"anti"`, `"co"`, `"meta"`), keep hyphen.
6. Otherwise, strip hyphen (`word1 + word2`).

### 7.3 The Four States

#### 1. `StateBodyText`
- **Heading Detection**:
  - Compute `medianBodyFontSize` across the document.
  - A line with `FontSize >= 1.45 * medianBodyFontSize` or matching `(?i)^(chapter|part|book|prologue|epilogue)\s+([0-9]+|[ivxlcdm]+|one|two|three|four|five|six|seven|eight|nine|ten)\b` emits `<h1>`.
  - `1.22 * medianBodyFontSize <= FontSize < 1.45 * medianBodyFontSize` emits `<h2>`.
  - Bold standalone single lines preceded by a larger vertical gap (`Δy > 1.4 * medianLinePitch`) and followed by body text emit `<h3>`.
- **Scene Breaks (`<hr class="scene-break"/>`)**:
  - A line with `<= 8` non-space characters consisting only of ornamental/separator symbols (`*`, `•`, `~`, `-`, `—`, `§`, `#`, `⋄`) centered within `±15%` of page center emits `<hr class="scene-break"/>`.
  - In `ProfileFiction`, an empty vertical gap between two body paragraphs on the same page where `Δy >= 2.6 * medianLinePitch` (and no heading/figure intervenes) also inserts `<hr class="scene-break"/>`.
- **Dialogue Preservation**:
  - In `ProfileFiction` (and general prose), if a line starts with an opening quotation mark or dialogue dash (`"`, `“`, `‘`, `—`, `–`, `«`, `'`), **never** merge it into the preceding paragraph; always start a fresh `<p>`.
- **Drop Caps**:
  - If a single-letter uppercase run has `Height >= 1.75 * medianBodyHeight` and sits at the top-left of a paragraph where the first 1–3 lines are indented immediately to its right (`line.Left >= dropCap.Right - 4`), merge it into the start of that paragraph: `<p><span class="dropcap">O</span>nce upon a time...</p>`.
- **List Items**:
  - Lines starting with bullets (`•`, `◦`, `▪`, `–`, `- `) or numbered markers (`1.`, `(a)`) are grouped into `<ul><li>...</li></ul>` or `<ol><li>...</li></ol>`. Continuation lines indented to align with the list text are fused into the active `<li>`.
- **Paragraph Fusion Rule**:
  - Merge `Line N+1` into the active `<p>` of `Line N` if and only if:
    1. `Δy = next.Top - curr.Top` is within `[0.75 * medianLinePitch, 1.45 * medianLinePitch]` (or across a page break when `Line N` is the last body line on Page P and `Line N+1` is the first body line on Page P+1),
    2. `Line N` reaches at least `82%` of the page's body text width (`curr.Right >= pageXRight - 0.18*bodyWidth`) OR `Line N` does not end with terminal sentence punctuation (`.`, `!`, `?`, `:`, `”`, `"`),
    3. `Line N+1` is not first-line indented (`next.Left <= pageXLeft + 1.2 * charW`), and
    4. `Line N+1` does not start with dialogue punctuation (`“`, `"`, `—`), a list marker, a heading, or a caption.

#### 2. `StateCodeBlock`
- **Entry Trigger**:
  - A line's dominant font is `IsMonospace == true`, OR
  - The line contains `>= 2` box-drawing / ASCII diagram characters (`┌`, `┐`, `└`, `┘`, `─`, `│`, `├`, `┤`, `┬`, `┴`, `┼`, `▼`, `▲`, `►`, `◄`, `╭`, `╮`, `╯`, `╰`), OR
  - In `ProfileTechnical`, `>= 2` consecutive indented lines exhibit code syntax tokens (`func `, `package `, `import `, `def `, `class `, `return `, `const `, `let `, `var `, `if (`, `for (`, `{`, `}`, `();`, `=>`, `->`, `//`).
- **Line Assembly**:
  - Collect all consecutive code/diagram lines (allowing blank vertical gaps up to `2.2 * medianLinePitch` if the next line also matches `StateCodeBlock`).
  - Find `minLeft = min(line.Left)` across the block.
  - For each line, compute `indentSpaces = int(math.Round((line.Left - minLeft) / monoCharWidth))` (clamped to `0..64`) and prepend `strings.Repeat(" ", indentSpaces)`.
  - Emit `<pre><code>...</code></pre>` (with XML-escaped text, preserving exact newlines).

#### 3. `StateTable`
- **Entry Trigger**:
  - `>= 2` consecutive `VisualLine`s with `HasMultipleColumns == true` (multiple horizontal text clusters separated by `> 2.2 * charW`) whose column X-stops align within `±18px`, OR a block immediately following a `Table X` caption.
- **Rendering Decision**:
  - Count distinct aligned column buckets (`numCols`).
  - **Simple Table (`numCols <= 3` and `len(rows) <= 30`)**:
    - Emit `<div class="table-wrap"><table>...</table></div>` using `<th>` for the first row if bold, and `<td>` for body cells.
  - **Complex Table (`numCols > 3` or irregular overlapping columns)**:
    - Compute bounding box `[minLeft, minTop, maxRight, maxBottom]` of the table rows on the page.
    - Call `CropPageRegionPNG` (`pdftoppm -png -r 300 -singlefile`) to render the exact table region to a crisp PNG, add the image to the EPUB via `book.AddImage`, and emit:
      ```xml
      <figure class="table-figure">
        <img src="../images/table_p004_01.png" alt="Table"/>
        <figcaption>Table caption (if present)</figcaption>
      </figure>
      ```
    - If `pdftoppm` fails for any reason, gracefully fall back to emitting the HTML `<table>`.

#### 4. `StateFigure`
- **Raster Images**:
  - When a page contains an `xmlImage` from `pdftohtml`, check for a caption line (`^(Figure|Fig\.|Diagram|Chart|Plate)\s+\d+` or italic/small centered text) within `45px` above or below the image box.
  - Bundle the image into the EPUB and emit `<figure><img src="..." alt="..."/><figcaption>...</figcaption></figure>` (absorbing the caption line so it isn't duplicated as a separate `<p>`).
- **Vector Diagram Recovery ("The Vector Trap")**:
  - Technical PDFs frequently draw flowcharts and plots using raw PDF vector operators (`m`, `l`, `c`, `re`, `S`), which `pdftohtml` does not export as `<image>` nodes. instead, they appear in `book.xml` either as:
    1. A `Figure X.Y` caption preceded or followed by a large vertical gap (`Δy >= 75px`) with no `<image>`, OR
    2. A cluster of short, scattered micro-labels (`<= 30` chars each, at varied X/Y coordinates) sandwiched between normal full-width paragraphs or above a `Figure X.Y` caption.
  - When detected:
    - Determine the vertical region `[diagramTop, diagramBottom]` between the previous body block and the caption (or subsequent body block) and horizontal span `[pageXLeft - 15, pageXRight + 15]`.
    - Suppress the scattered diagram micro-labels from being emitted as garbled `<p>` text.
    - Render `[diagramLeft, diagramTop, diagramWidth, diagramHeight]` at 300 DPI using `CropPageRegionPNG` (`pdftoppm`), register the PNG with `book.AddImage`, and emit `<figure><img src="..." alt="..."/><figcaption>...</figcaption></figure>`.

---

## 8. Multi-Chapter Packaging & Reader Stylesheet (`internal/converter/pdf_converter.go`, `internal/converter/converter.go`)

### 8.1 Multi-Chapter Splitting & Native TOC

1. **Chapter Boundaries**:
   - **Strategy A (PDF Outline)**: If `book.xml` contains `<outline><item page="N">Title</item></outline>`, start a new chapter section at the corresponding page/heading and use the outline title.
   - **Strategy B (Detected `<h1>` Headings)**: Whenever `StateBodyText` emits a chapter-level `<h1>` (or every 20 pages if a long PDF has neither an outline nor `<h1>` headings), start a new `ChapterSection`.
2. **Strict Validation**:
   - Before calling `book.AddSection(chapterXHTML, chapterTitle, fmt.Sprintf("chapter_%03d.xhtml", i+1), cssPath)`, pass `chapterXHTML` through `validateXHTMLFragment(chapterXHTML)`.
   - If validation ever fails (e.g., an unexpected nested tag), run `SanitizeToXHTML` on `chapterXHTML` as a guaranteed safety net so `book.AddSection` never receives invalid XML.

### 8.2 Stylesheet Additions (`epubStylesheet` in `internal/converter/converter.go`)

Append the following rules to `epubStylesheet`:

```css
hr.scene-break {
	border: none;
	text-align: center;
	margin: 1.8em auto;
	height: 1.5em;
}

hr.scene-break::after {
	content: "*   *   *";
	letter-spacing: 0.4em;
	opacity: 0.75;
}

.dropcap {
	float: left;
	font-size: 3.1em;
	line-height: 0.85;
	padding-top: 0.04em;
	padding-right: 0.1em;
	padding-bottom: 0.02em;
	font-weight: bold;
}

.table-figure img {
	max-width: 100%;
	height: auto;
	margin: 0.8em auto;
}

.table-wrap {
	overflow-x: auto;
	margin: 1.2em 0;
}
```

---

## 9. Bubble Tea TUI Updates (`internal/tui/tui.go`)

1. **Prompt & Subtitle**:
   - Update header subtitle to: `"Web Article & PDF to EPUB Converter (Apple Books / Kindle)"`.
   - Update input prompt label to: `"Paste a web URL or local PDF path (drag & drop supported):"` and prompt prefix to `"Input > "` (placeholder: `"https://example.com/post or ~/Books/novel.pdf"`).
2. **Smart PDF Detection & Profile Selector**:
   - Add `profile converter.BookProfile` to `tui.Model` (initialized to `converter.ProfileAuto`).
   - Helper `isPDFInput(val string) bool`: returns `true` when the trimmed/unquoted input ends with `.pdf` (case-insensitive) or starts with `/`, `~/`, `file://`.
   - When `m.state == stateInput` and `isPDFInput(m.input.Value())` is `true`, render an interactive profile selector row directly below the text input inside the box:
     ```
     Book Profile:  [● Auto-Detect]   ○ Fiction   ○ Technical     (tab: switch profile)
     ```
   - Pressing `tab` or `shift+tab` while in `stateInput` cycles `m.profile` between `ProfileAuto` → `ProfileFiction` → `ProfileTechnical`.
3. **Dynamic Pipeline Step Labels**:
   - Add `isPDF bool` to `tui.Model` (set when conversion starts).
   - When `m.isPDF == true`, display PDF-specific step labels in `stateConverting`:
     - `StepFetching`: `"Inspect PDF & extract spatial layout via Poppler"`
     - `StepExtracting`: `"Remove headers/footers & reconstruct reading flow"`
     - `StepLocalizingAssets`: `"Render figures, code blocks, tables & validate XHTML"`
     - `StepPackaging`: `"Package multi-chapter EPUB with reader stylesheet"`
4. **Completion Summary (`stateDone`)**:
   - If `res.PagesTotal > 0`, include `Pages / Chapters` and `Profile` lines in the green success summary box:
     ```
     Title:    PDF to EPUB Conversion Challenges
     Author:   Unknown
     Format:   6 pages → 2 chapters (profile: technical)
     Images:   3 bundled (of 3 found)
     Saved:    /Users/sunday/Documents/PDF to EPUB Conversion Challenges.epub
     Time:     420ms
     ```

---

## 10. Step-by-Step Implementation Order for the Implementing Agent

### Step 1: Extend Types & Input Detection (`internal/converter/converter.go`)
- Add `InputKind`, `BookProfile`, `ConvertOptions`, and new fields on `Result` (`PagesTotal`, `Chapters`, `ProfileUsed`).
- Implement `DetectInputKind(raw string) (InputKind, string, error)` handling shell-escaped drag-and-drop paths, `~/`, `file://`, `.pdf` URLs, and `%PDF-` magic bytes.
- Add `ConvertWithOptions` while keeping `Convert` as a wrapper.
- Update `epubStylesheet` with `.scene-break`, `.dropcap`, `.table-figure`, and `.table-wrap`.

### Step 2: Implement Poppler Wrapper (`internal/converter/pdf_poppler.go`)
- Implement `findPopplerBin`, `runPDFInfo`, `runPDFToHTMLXML`, `parsePopplerXML`, font prefix stripping & classification (`IsMonospace`, `IsBold`, `IsItalic`), and `CropPageRegionPNG` (`pdftoppm -png -r 300 -singlefile`).

### Step 3: Implement Spatial Layout & Noise Stripping (`internal/converter/pdf_layout.go`)
- Implement Unicode ligature expansion (`ﬀ`, `ﬁ`, `ﬂ`, `ﬃ`, `ﬄ`).
- Implement `BuildVisualLines` (horizontal baseline grouping within `±3.5px`, inline `<code>`/`<strong>`/`<em>`/`<a>` wrapping, and multi-column gap detection).
- Implement `StripRunningNoise` (cross-page header/footer template matching, page number stripping, and browser print header/footer removal).
- Implement `SplitPageColumns` (2-column gutter detection).

### Step 4: Implement State Machine & Reflow Engine (`internal/converter/pdf_statemachine.go`)
- Implement `ResolveBookProfile` (`Auto` → `Fiction` or `Technical`) and `BuildDocumentLexicon` (document-wide word & hyphenation frequency map).
- Implement `StateBodyText` (headings, scene breaks, dialogue line separation, drop caps, lists, paragraph fusion, and lexicon-backed de-hyphenation).
- Implement `StateCodeBlock` (monospace & ASCII box-drawing diagram preservation with calculated leading space indentation).
- Implement `StateTable` (`<=3` columns → semantic HTML `<table>`; `>3` columns → 300-DPI PNG via `CropPageRegionPNG` with HTML fallback).
- Implement `StateFigure` (raster `<image>` + caption pairing, plus vector diagram gap detection & `CropPageRegionPNG`).

### Step 5: Implement PDF-to-EPUB Orchestrator (`internal/converter/pdf_converter.go`)
- Wire Steps 2–4 together into `convertPDF(ctx, pdfPath, sourceLabel, opts, onProgress) (*Result, error)`.
- Split output into multi-chapter XHTML sections (`chapter_001.xhtml`, ...) using `<outline>` or `<h1>` boundaries.
- Validate every chapter with `validateXHTMLFragment` and package with `go-shiori/go-epub`.

### Step 6: Update TUI (`internal/tui/tui.go`)
- Add `Tab` profile cycling (`Auto` / `Fiction` / `Technical`) when input looks like a PDF, dynamic pipeline step labels, and PDF stats in the completion view.

### Step 7: Write Comprehensive Tests (`internal/converter/pdf_converter_test.go`)
- **Unit tests (no external binary required)**:
  - `TestDetectInputKind`: URLs, remote `.pdf` URLs, local paths, Finder drag-and-drop escaped paths (`'/Users/test/My\ Book.pdf'`).
  - `TestLigatureAndFontClassification`: `FAAAAA+Menlo` → monospace, `ﬁ` → `fi`.
  - `TestBuildVisualLinesAndStripRunningNoise`: synthetic `xmlPage` slices verifying inline `<code>` coalescing and cross-page header/footer removal.
  - `TestFictionStateMachine`: dialogue lines starting with `“` never merge; `* * *` becomes `<hr class="scene-break"/>`; drop caps wrap in `<span class="dropcap">`; lexicon de-hyphenation keeps `well-known` and joins `re-flowable`.
  - `TestTechnicalStateMachine`: monospace & box-drawing blocks emit `<pre><code>` with preserved indentation; 2-col table emits `<table>`; 5-col table triggers region cropper callback.
- **Integration test (runs if `pdftohtml` is available)**:
  - Generate or use a multi-page fixture PDF, run `ConvertWithOptions`, unzip the resulting `.epub`, and verify with `encoding/xml` that every `chapter_*.xhtml` is strictly valid XML.

---

## 11. Verification Checklist

Run these commands from `/Users/sunday/Dev/Personal/Projects/web2reader`:

```bash
# 1. Run all existing and new unit/integration tests
make test

# 2. Build binary into ./bin/rebind
make build

# 3. Smoke-test PDF conversion against the reference PDF
go test -v ./internal/converter -run TestPDF
```
