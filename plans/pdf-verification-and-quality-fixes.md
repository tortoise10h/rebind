# PDF-to-EPUB Agent Verification Toolkit & Quality Fixes — Handover Implementation Plan

> **For the Implementing Agent:** This plan is self-contained and grounded in exact line numbers, struct definitions, Poppler XML coordinates, and edge cases from [`sample/harry-potter-original-pdf.pdf`](file:///Users/sunday/Dev/Personal/Projects/rebind/sample/harry-potter-original-pdf.pdf), [`sample/harry-potter-calibre.epub`](file:///Users/sunday/Dev/Personal/Projects/rebind/sample/harry-potter-calibre.epub), and [`sample/harry-potter-rebind.epub`](file:///Users/sunday/Dev/Personal/Projects/rebind/sample/harry-potter-rebind.epub). Follow **Phase 1** first to build the verification & inspection CLI, then execute **Phase 2** using the verification tool after each fix.

---

## 1. Goal & Scope

1. **Phase 1 — Build a 100% Non-Blocking Agent Verification & Inspection Workflow**:
   - Add headless CLI flags (`--headless`, `-o`/`--output`, `--profile`, `--pages`) to [`main.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/main.go) so agents can convert full PDFs or specific page ranges (`--pages 15-16`) from the shell in `<200ms` without launching the interactive Bubble Tea TUI.
   - Build a dedicated agent verification & diagnostic CLI in `cmd/verify` (`inspect` and `audit` subcommands) plus `Makefile` targets (`make verify`, `make inspect`) that give agents X-ray visibility into every pipeline stage and score any generated EPUB against both the source PDF and Calibre's baseline output.
2. **Phase 2 — Fix All 5 Real-World PDF Conversion Bugs & Surpass Calibre**:
   - Use `cmd/verify` to fix the 5 systemic defects discovered in [`sample/harry-potter-rebind.epub`](file:///Users/sunday/Dev/Personal/Projects/rebind/sample/harry-potter-rebind.epub) and regenerate [`sample/harry-potter-rebind.epub`](file:///Users/sunday/Dev/Personal/Projects/rebind/sample/harry-potter-rebind.epub) so it cleanly surpasses [`sample/harry-potter-calibre.epub`](file:///Users/sunday/Dev/Personal/Projects/rebind/sample/harry-potter-calibre.epub) across every quality metric.

---

## 2. Exact Root-Cause Analysis of Current Bugs in `sample/`

| # | Defect in `harry-potter-rebind.epub` | Exact Root Cause in Codebase |
| :--- | :--- | :--- |
| **1** | **30/30 Chapter Opening Sentences Scrambled by 3-Line Drop Caps**<br/>e.g. Ch 1 (`p.15`): `<p>I through his brain without leaving the slightest trace of meaning bet was nearing midnight...</p><p>hind. He was waiting...</p>`<br/>e.g. Ch 28 (`p.611`): `<p>H “Out of here, quickly,” said Snape.</p><p>arry felt as though...</p>` | Every chapter in `harry-potter-original-pdf.pdf` starts with a 3-line initial drop-cap at `top=433..435, left=72, height=94` (`bottom=528..529`).<br/>• Line 1 of body text is at `top=457..458, height=25` (`bottom=483`).<br/>• Line 2 is at `top=482..483, height=25` (`bottom=508`).<br/>• Line 3 is at `top=507..508, height=25` (`bottom=532..533`).<br/>In [`BuildVisualLines`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_layout.go#L240-L265), `runs` are sorted by `Top` (`I` is `clusters[0]`). When Line 3 (`bottom=533`) is checked against `clusters`, `minH = min(94, 25) = 25`, `0.25*minH = 6.25`, and `baselineDiff = |528 - 533| = 5.0 <= 6.25`! So **Line 3 merges into `clusters[0]` with the drop-cap `I`**, inherits `minTop=434`, and sorts *above* Line 1 (`top=458`).<br/>Even worse: [`renderParagraphBlock`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_statemachine.go#L926-L942) *already* has `Case 1` for a standalone 1-letter drop-cap line, which never triggered because `clusters[0]` was polluted with Line 3's text—and `Case 1` also bypassed the de-hyphenation loop (`lines 960–1002`). |
| **2** | **0 Embedded Images & Missing Cover (`Images in zip: 0` vs 37 real illustrations)** | [`runPDFToHTMLXML`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_poppler.go#L96-L112) passes an absolute path `outXML = filepath.Join(tmpDir, "book.xml")` to `pdftohtml`. Poppler writes `<image src="/var/folders/.../book-15_1.png"/>` with an **already-absolute path**. Then [`addImage`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_converter.go#L147) and cover detection ([`pdf_converter.go:192`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_converter.go#L192)) call `filepath.Join(tmpDir, src)`, doubling `tmpDir` so `os.Stat` fails 100% of the time.<br/>**Secondary trap once fixed:**<br/>1. Page 1 (`width=216, height=999`) is a narrow spine/flap, while **Page 2 (`width=681, height=999`) is the true front cover** (`book-2_1.jpg`).<br/>2. **Page 7 (Title Page)** contains **231 1px-tall sliced horizontal raster strips** (`book-7_1.png..book-7_233.png`) for the artwork. Calibre dumps all 231 strips as separate `<p><img/></p>` tags. We must detect stacked micro-strips and crop them as a single composite image via `pdftoppm`.<br/>3. In [`parseSinglePage`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_statemachine.go#L293-L344), Step A emits all `BlockFigure` blocks *before* Step C emits headings/paragraphs, which places the chapter illustration *above* `<h1>CHAPTER ONE</h1>` instead of at its true vertical position between `CHAPTER ONE` (`top=68`) and `THE OTHER MINISTER` (`top=349`). |
| **3** | **264 Mid-Sentence Cross-Page Paragraph Breaks** | [`ParsePDFPages`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_statemachine.go#L274-L283) runs `parseSinglePage` on each page independently. Whenever a sentence wraps from the bottom of Page $N$ to the top of Page $N+1$ (e.g., p.15→16: `<p>...How on earth</p><div id="p16"></div><p>was his government supposed to...</p>`), it emits two separate `<p>` blocks separated by `<div id="p16"></div>`, creating visible paragraph breaks and first-line indents mid-sentence. |
| **4** | **Shifted End-of-Book Chapter Titles (`chapter_034` titled `"vii"`) & Page-Number Noise in TOC** | In Poppler's `<outline>` XML, nested `<outline>` blocks are siblings of `<item>`:<br/>`<item page="9">Contents</item><outline><item page="9">vii</item>...<item page="12">x</item></outline><item page="15">The Other Minister · 1</item>...<item page="667">Credits</item>`<br/>Because [`xmlOutline`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_poppler.go#L157-L160) unmarshals `Items []xmlOutlineItem` and `Outlines []xmlOutline` into separate slices, [`Flatten()`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_poppler.go#L173-L198) appends all top-level `Items` (`pages 8..667`) **first**, and then appends `Outlines` (`vii..x`, `pages 9..12`) **at the very end of the slice after `Credits` (p.667)**! When [`splitBlocksIntoChapters`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_converter.go#L289) processes the second block on Page 667, `b.PageNum (667) >= bookmarks[33].Page (9, "vii")` evaluates to `true`, splitting Page 667 into `chapter_033` (`Credits`) and `chapter_034` (`vii`)!<br/>Additionally, bookmark titles retain print page numbers (`The Other Minister · 1`). |
| **5** | **`<h1>` Heading Explosion (`119` `<h1>` tags across 34 files)** | [`isHeading1`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_statemachine.go#L537-L552) marks every line with `FontSize >= 1.45*medianFontSize` as a standalone `<h1>`:<br/>• Consecutive heading lines on chapter openings (`<h1>C H A P T E R  O N E</h1>` + `<h1>THE OTHER MINISTER</h1>`, or 2-line titles `<h1>HERMIONE’S</h1>` + `<h1>HELPING HAND</h1>`) are not merged.<br/>• On the Contents pages (p.9–12), chapter numbers `ONE`..`THIRTY` (`font size=39`) become 30 separate `<h1>` tags.<br/>• On Page 8 (Dedication), all 4 lines (`To Mackenzie, / My beautiful daughter, / I dedicate / Her ink-and-paper twin.`) become 4 `<h1>` tags.<br/>• On Page 667 (Colophon), `This / book was art / directed by David Saylor.` and `The art for both...` are split into two paragraphs because line spacing is `32px > 1.6 * medianPitch (25px)`. |

---

## 3. Phase 1 Implementation Plan: Headless CLI & Verification Tool (`cmd/verify`)

### Step 1.1: Extend `ConvertOptions` & Add Page-Range Support in `internal/converter`

1. **Modify [`internal/converter/converter.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/converter.go#L59-L62)**:
   ```go
   type ConvertOptions struct {
       OutputDir  string
       OutputPath string      // Optional explicit output .epub path (overrides OutputDir + title)
       Profile    BookProfile // Default: ProfileAuto (only used for PDF inputs)
       FirstPage  int         // Optional 1-based start page for PDF partial conversion (0 = start)
       LastPage   int         // Optional 1-based end page for PDF partial conversion (0 = end)
   }
   ```
2. **Modify [`internal/converter/pdf_poppler.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_poppler.go#L96-L112)**:
   - Add `runPDFToHTMLXMLRange(ctx context.Context, pdftohtmlBin, pdfPath, tmpDir string, firstPage, lastPage int) (string, error)` (and keep `runPDFToHTMLXML` calling it with `0, 0`).
   - When `firstPage > 0`, pass `"-f", strconv.Itoa(firstPage)` to `pdftohtml`.
   - When `lastPage > 0`, pass `"-l", strconv.Itoa(lastPage)` to `pdftohtml`.
3. **Modify [`internal/converter/pdf_converter.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_converter.go#L241-L246)**:
   - Pass `opts.FirstPage, opts.LastPage` into `runPDFToHTMLXMLRange`.
   - When `opts.OutputPath != ""` is provided, write directly to `opts.OutputPath` (creating parent directories if needed via `os.MkdirAll(filepath.Dir(opts.OutputPath), 0o755)`) instead of `uniqueOutputPath(opts.OutputDir, filename)`.
   - Fix `for pIdx, lines := range pages` in [`ParsePDFPages`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_statemachine.go#L274-L280) so `pageNum` uses `doc.Pages[pIdx].Number` (if `doc != nil && pIdx < len(doc.Pages) && doc.Pages[pIdx].Number > 0`) instead of `pIdx + 1`! (Critical when converting a slice like `--pages 15-16` where `pIdx=0` is actually Page 15).

### Step 1.2: Create Inspection API in `internal/converter/pdf_inspect.go`

Create [`internal/converter/pdf_inspect.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_inspect.go) to expose intermediate pipeline state to `cmd/verify`:

```go
package converter

type PageInspection struct {
	PageNum      int          `json:"page_num"`
	Width        float64      `json:"width"`
	Height       float64      `json:"height"`
	RawTexts     []RawRunDump `json:"raw_texts"`
	RawImages    []RawImgDump `json:"raw_images"`
	RawLines     []VisualLine `json:"raw_lines"`      // Before StripRunningNoise
	CleanLines   []VisualLine `json:"clean_lines"`    // After StripRunningNoise
	NoiseRemoved []VisualLine `json:"noise_removed"`  // Lines stripped as headers/footers/page nums
	Blocks       []ParsedBlock `json:"blocks"`
}

type InspectionReport struct {
	PDFPath        string           `json:"pdf_path"`
	Title          string           `json:"title"`
	Author         string           `json:"author"`
	TotalPages     int              `json:"total_pages"`
	ProfileUsed    BookProfile      `json:"profile_used"`
	MedianFontSize float64          `json:"median_font_size"`
	MedianPitch    float64          `json:"median_pitch"`
	MedianHeight   float64          `json:"median_height"`
	Bookmarks      []Bookmark       `json:"bookmarks"`
	Pages          []PageInspection `json:"pages"`
}

func InspectPDFPages(ctx context.Context, pdfPath string, firstPage, lastPage int, profile BookProfile) (*InspectionReport, error)
```

### Step 1.3: Add Headless CLI Flags to `main.go`

Update [`main.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/main.go):
- Parse flags:
  - `--headless` (`bool`): Run without Bubble Tea TUI, logging step progress to `stderr` and final summary to `stdout`.
  - `-o` and `--output` (`string`): Output `.epub` file path (if ending in `.epub`) or output directory.
  - `--profile` (`string`): `auto`, `fiction`, or `technical`.
  - `--pages` (`string`): Optional `<start>-<end>` or `<page>` range (e.g., `15-16` or `15`).
- If `--headless` is false (and `-o`/`--pages` are not set), preserve the exact existing Bubble Tea startup (`tea.NewProgram(tui.NewModel(initialURL, outputDir))`).

### Step 1.4: Build `cmd/verify/main.go` (`inspect` and `audit` Subcommands)

Create `cmd/verify/main.go` with two subcommands:

#### 1. `go run ./cmd/verify inspect`
Flags:
- `--pdf <path>` (required)
- `--pages <M-N>` (default `1-5`)
- `--profile <auto|fiction|technical>` (default `auto`)
- `--stage <all|xml|lines|blocks|xhtml>` (default `all`)
- `--json` (optional JSON output)

Prints each page's raw `<text>`/`<image>` runs (with `Top`, `Left`, `Width`, `Height`, `Bottom=Top+Height`, `FontSize`, `FontFamily`), `VisualLine`s, stripped noise lines, and `ParsedBlock`s so an agent can debug any page in `<150ms`.

#### 2. `go run ./cmd/verify audit`
Flags:
- `--pdf <path>` (default `sample/harry-potter-original-pdf.pdf`)
- `--epub <path>` (default `sample/harry-potter-rebind.epub`)
- `--calibre <path>` (optional, default `sample/harry-potter-calibre.epub` if present)
- `--json` (emit machine-readable JSON report)
- `--strict` (exit code `1` if any critical check fails)

Implements **6 deterministic checks**:
1. **Strict XML & Link/Asset Integrity**:
   - Opens the `.epub` ZIP archive, parses every `.xhtml`, `.html`, `.opf`, `.ncx`, and `container.xml` with `encoding/xml` (`xml.NewDecoder` with `Strict = false` only for HTML entities if testing Calibre, and strict XML check for `rebind`).
   - Verifies every `<a href="...">` internal link target file and `#anchor` (`id="..."`) exists in the target file.
   - Verifies every `<img src="...">` resolves to a valid, non-zero-byte image in the ZIP archive.
   - Checks for escaped raw HTML tag strings (`&lt;a `, `&lt;i&gt;`, `&lt;em&gt;`, `&lt;strong&gt;`).
2. **Drop-Cap & Opening Sentence Integrity**:
   - Scans chapter paragraphs for:
     - **Scrambled 3-line drop-cap signatures**:
       - `<p>` starting with a single uppercase letter followed by space and a lowercase word (`^([B-HJ-Z])\s+[a-z]`) — **Note:** exclude valid English words `"A"` and `"I"` unless the second paragraph of the chapter starts with a word fragment or lowercase letter!
       - Any `<p>` starting with a single uppercase letter (or `<span class="dropcap">X</span>`) where the **next `<p>` in the same chapter starts with a lowercase letter `[a-z]`** (e.g., `<p>I through his brain...</p><p>hind. He was waiting...</p>` or `<p>H “Out of here...”</p><p>arry felt...</p>`). This catches **100% (30/30)** of the Harry Potter chapter scrambles with zero false positives!
       - Any `<p>` starting with a lowercase letter `[a-z]` or apostrophe fragment (`’mere`).
3. **Cross-Page Paragraph Continuity**:
   - Counts every page anchor boundary (`<div id="p\d+"></div>` or `<span id="p\d+"></span>`) where a paragraph closes immediately before the page anchor without terminal sentence punctuation (`.`, `!`, `?`, `"`, `”`, `’`, `…`, `:`) OR the paragraph immediately after the page anchor starts with a lowercase letter `[a-z]`.
4. **Heading & TOC Hygiene**:
   - Counts total `<h1>` tags vs total chapter files; flags chapters with `> 1` `<h1>` tag or consecutive `<h1>...</h1>\s*<h1>...</h1>`.
   - Checks `nav.xhtml` / `toc.ncx` for:
     - Print page number suffixes (`· \d+`).
     - Out-of-order / misplaced Roman numeral TOC items (`vii`, `viii`, `ix`, `x` appearing at the end of the book).
5. **Image & Cover Fidelity**:
   - Runs `pdfimages -list <pdf>` to count source pages with raster images.
   - Checks whether the EPUB has a valid cover image (`meta name="cover"` or `properties="cover-image"`).
   - Counts total packaged images and flags **Micro-Slice Spam** (any chapter/page containing `>= 10` consecutive images with height `<= 5px`, which flags Calibre's 231 sliced images on page 7).
6. **Side-by-Side Comparison Table (`rebind` vs `Calibre`)**:
   - Prints a Markdown table comparing both EPUBs on:
     - XML Validity Errors
     - Broken Internal Links
     - Scrambled Chapter Openings / Drop Caps
     - Mid-Sentence Cross-Page Paragraph Splits
     - Total Chapters / Semantic `<h1>` Ratio
     - Clean TOC Titles (no `· page` noise / no shifted titles)
     - Embedded Illustrations & Micro-Slice Spam Count

### Step 1.5: Update `Makefile` & Remove Stray `-.xml`
- Add `verify` and `inspect` targets to [`Makefile`](file:///Users/sunday/Dev/Personal/Projects/rebind/Makefile):
  ```makefile
  ## verify: Convert sample PDF headlessly and run full audit against Calibre baseline
  verify: build
  	@./$(BUILD_DIR)/$(BINARY_NAME) --headless -o sample/harry-potter-rebind.epub sample/harry-potter-original-pdf.pdf
  	@go run ./cmd/verify audit --pdf sample/harry-potter-original-pdf.pdf --epub sample/harry-potter-rebind.epub --calibre sample/harry-potter-calibre.epub

  ## inspect: Inspect pipeline stages for specific pages (e.g. make inspect PAGES=15-16)
  inspect:
  	@go run ./cmd/verify inspect --pdf sample/harry-potter-original-pdf.pdf --pages $(or $(PAGES),15-16)
  ```
- Delete the stray [`-.xml`](file:///Users/sunday/Dev/Personal/Projects/rebind/-.xml) file from the workspace root.

---

## 4. Phase 2 Implementation Plan: Fix All 5 PDF Conversion Defects

### Step 2.1: Fix Multi-Line Drop Caps in `BuildVisualLines` & `renderParagraphBlock`

1. **In [`internal/converter/pdf_layout.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_layout.go#L240-L265) (`BuildVisualLines`)**:
   - Prevent a multi-line drop-cap run from clustering with a normal body line that happens to share its bottom baseline:
     ```go
     minH := math.Min(last.Height, run.Height)
     maxH := math.Max(last.Height, run.Height)
     baselineDiff := math.Abs((last.Top + last.Height) - (run.Top + run.Height))
     topDiff := math.Abs(last.Top - run.Top)

     // A multi-line drop cap (e.g. height=94 vs body height=25) spans 3 lines:
     // its bottom aligns with Line 3 while its top is near Line 1.
     // Never merge two runs of drastically different height if their tops are far apart!
     isMultiLineDropCapMismatch := maxH >= 1.8*minH && topDiff > 0.6*minH

     if !isMultiLineDropCapMismatch && (baselineDiff <= math.Max(3.5, 0.25*minH) || topDiff <= 3.5) {
         clusters[idx] = append(clusters[idx], run)
         placed = true
         break
     }
     ```
   - Notice how this preserves single-line raised initials on Page 7 (`H` `h=75` + `arry` `h=69`, `maxH/minH=1.08`), Page 8 (`T` `h=37` + `o Mackenzie` `h=23`, `maxH/minH=1.61`), and Page 9 (`C` `h=62` + `o n t e n t s` `h=50`, `maxH/minH=1.24`), while keeping 3-line chapter drop-caps (`h=94` vs `h=25`, `maxH/minH=3.76`, `topDiff=74`) separate from Line 3!
2. **In [`internal/converter/pdf_statemachine.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_statemachine.go#L850-L908) (`collectParagraphLines`)**:
   - When `startIdx` is a standalone single-letter drop-cap (`isDropCapLead == true`, e.g., `I` at `top=434, height=94`), `lines[startIdx+1]` (`t was nearing...` at `top=458`) is added first, and then `isDropCapLead` becomes `false`.
   - However, in Ch 28 (`p.611`: `<span class="dropcap">H</span>arry felt...` on lines 1–2, followed by `“Out of here, quickly,” said Snape.` on line 3) and Ch 29 (`p.625`: `<span class="dropcap">C</span>’mere, Harry . . .”` on line 1, followed by `“No.”` on line 2), `dialogueLeadRegex` properly breaks before the new speaker's dialogue line!
   - Also check the indentation guard in `collectParagraphLines` ([`pdf_statemachine.go:898-901`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_statemachine.go#L898-L901)):
     - Lines 1 and 2 beside a 3-line drop-cap are indented to the right of the drop-cap (`Left = 102..169`), while Line 3 wraps back to the normal left margin (`Left = 72..94`). Because `curr.Left < prev.Left`, `curr.Left > prev.Left+1.8*charW` is false, so Line 3 seamlessly continues the opening paragraph!
3. **In [`internal/converter/pdf_statemachine.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_statemachine.go#L920-L1005) (`renderParagraphBlock`)**:
   - Extract the line-fusion + `Lexicon.Dehyphenate` loop (`lines 945–1004`) into a helper `fuseParagraphLines(lines []VisualLine, lex *DocumentLexicon) string`.
   - In `Case 1` (`len(lines) >= 2 && len(firstTrimmed) == 1 && unicode.IsUpper(rune(firstTrimmed[0])) && lines[0].FontSize >= 1.5*medianFontSize`):
     - Call `rest := fuseParagraphLines(lines[1:], config.Lexicon)` instead of the raw loop that skipped de-hyphenation!
     - Join `<span class="dropcap">%c</span>` directly with `rest` (no space when `rest` starts with a lowercase letter or apostrophe like `t was`, `arry`, `’mere`; if `rest` starts with an uppercase word where `r` was a standalone word `"I"` or `"A"`, add a space).

### Step 2.2: Fix Image Path Resolution, Cover Detection, Micro-Strip Cropping & Figure Ordering

1. **Fix `tmpDir` Path Doubling in [`internal/converter/pdf_converter.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_converter.go#L139-L204)**:
   - Add a helper:
     ```go
     func resolvePopplerImagePath(tmpDir, src string) (string, error) {
         if filepath.IsAbs(src) {
             if _, err := os.Stat(src); err == nil {
                 return src, nil
             }
         }
         candidate := filepath.Join(tmpDir, filepath.Base(src))
         if _, err := os.Stat(candidate); err == nil {
             return candidate, nil
         }
         candidate2 := filepath.Join(tmpDir, src)
         if _, err := os.Stat(candidate2); err == nil {
             return candidate2, nil
         }
         return "", os.ErrNotExist
     }
     ```
   - Use `resolvePopplerImagePath(tmpDir, src)` in both `addImage` and cover detection.
2. **Enhance Cover Image Detection ([`pdf_converter.go:187-204`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_converter.go#L187-L204))**:
   - Scan the first `min(3, len(doc.Pages))` pages for the first full-bleed portrait cover image (`p.Width >= 300 && img.Width >= 0.65*p.Width && img.Height >= 0.65*p.Height`).
   - Record which `(pageIdx, imgIdx)` was used as the cover, and mark full-bleed cover/flap/back-cover pages (Pages 1, 2, 3, 670, 671 where a single image covers `>= 65%` of the page and there is no meaningful body text) so they don't create empty/duplicate front-matter blocks before `Copyright`.
3. **Coalesce 1px Sliced Image Strips & Preserve Vertical Figure Position ([`pdf_statemachine.go:288-344`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_statemachine.go#L288-L344))**:
   - In `parseSinglePage`:
     - Before processing images, group `images` that are vertically contiguous micro-strips (`img.Height <= 8` or `|next.Top - (curr.Top+curr.Height)| <= 3.0` with overlapping horizontal span).
     - If a group has `>= 4` stacked strips (such as Page 7's 231 strips spanning `top=221..490, left=225, width=185`), replace the 231 individual `addImage` calls with a **single** `config.CropRegion(pageNum, minLeft, minTop, maxRight-minLeft, maxBottom-minTop, ...)` call!
     - Track the `Top` coordinate on every `ParsedBlock` (`Top float64` field on `ParsedBlock`) and sort `blocks` on a page by `Top` ascending at the end of `parseSinglePage` (before heading coalescing) so chapter illustrations (`top=94`) appear in their true visual position **between** `CHAPTER ONE` (`top=68`) and `THE OTHER MINISTER` (`top=349`), rather than above `CHAPTER ONE`!

### Step 2.3: Fuse Cross-Page Mid-Sentence Paragraphs (`pdf_converter.go`)

In [`internal/converter/pdf_converter.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_converter.go), add `mergeCrossPageParagraphs(blocks []ParsedBlock, lex *DocumentLexicon) []ParsedBlock` and call it in `finalizeChapters` (or before `splitBlocksIntoChapters` on blocks within the same chapter):
- When `prevBlock` (`Kind == BlockParagraph`, `PageNum == P`) is immediately followed by `currBlock` (`Kind == BlockParagraph`, `PageNum == P+1`):
  - Extract plain text of `prevBlock` and `currBlock` (using `stripHTMLTags`).
  - Let `prevEndsSentence = endsWithSentencePunct(prevPlain)` (where terminal punctuation is `.`, `!`, `?`, `"`, `”`, `’`, `:`).
  - Let `currStartsLower = len(currPlain) > 0 && unicode.IsLower(firstRune(currPlain))`.
  - If `!prevEndsSentence || currStartsLower`:
    - Merge `currBlock` into `prevBlock`!
    - Insert `<span id="p<currBlock.PageNum>"></span>` right at the join boundary inside the `<p>...</p>` (and mark `seenPages[currBlock.PageNum] = true` in `finalizeChapters` so a duplicate `<div id="p..."></div>` is not emitted outside `<p>`).
    - If `prevPlain` ends with a hyphen `-`, de-hyphenate the boundary words using `lex.Dehyphenate(lastWord, firstWord)` before inserting `<span id="pN"></span>`.

### Step 2.4: Fix Outline Unmarshaling (`chapter_034` `"vii"` Bug) & Clean TOC Titles (`pdf_poppler.go` & `pdf_converter.go`)

1. **In [`internal/converter/pdf_poppler.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_poppler.go#L157-L198)**:
   - Because Poppler's `<outline>` nests sub-toc `<outline>` elements as siblings of `<item>` (putting `vii..x` in `out.Outlines`), update `xmlOutline.Flatten()` to **only use top-level `out.Items`** (or filter out any bookmark whose `Page` is `< prevBookmark.Page` or whose title matches Roman numerals `^(?i)[ivxlcdm]+$`).
   - Also clean trailing print page number noise from bookmark titles:
     ```go
     var tocPageSuffixRegex = regexp.MustCompile(`\s*[·•]\s*\d+\s*$`)
     ```
     Strip `tocPageSuffixRegex` in `Flatten()` so `The Other Minister · 1` becomes `The Other Minister`.
2. **In [`internal/converter/pdf_converter.go`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_converter.go#L289)**:
   - Change `if bookmarkIdx+1 < len(bookmarks) && b.PageNum >= bookmarks[bookmarkIdx+1].Page` to a `for` loop (`for bookmarkIdx+1 < len(bookmarks) && b.PageNum >= bookmarks[bookmarkIdx+1].Page { bookmarkIdx++; currentTitle = bookmarks[bookmarkIdx].Title }`) so even if a bookmark page has no blocks, `bookmarkIdx` never lags behind `b.PageNum`.

### Step 2.5: Coalesce Multi-Line Headings, Contents Page & Colophon (`pdf_statemachine.go`)

1. **Coalesce Multi-Line `BlockHeading1` / `BlockHeading2` on the Same Page**:
   - In `parseSinglePage` (after sorting blocks by `Top`):
   - When consecutive blocks on the same page are `BlockHeading1` (e.g., `<h1>HERMIONE’S</h1>` at `top=349` and `<h1>HELPING HAND</h1>` at `top=389`, or `<h1>C H A P T E R  O N E</h1>` and `<h1>THE OTHER MINISTER</h1>` if no image is between them, or `<h1>Harry Potter</h1>` and `<h1>AND THE HALF-BLOOD PRINCE</h1>`), merge them into a single `<h1>` (joining with `<br/>` or space).
   - If a chapter opening has `<h1>C H A P T E R  O N E</h1>`, followed by `<figure><img.../></figure>`, followed by `<h1>THE OTHER MINISTER</h1>`, convert the second heading (`THE OTHER MINISTER`) into `<h2>THE OTHER MINISTER</h2>` (or keep `<h1>` on the chapter title and `<p class="chapter-num">` / `<h2>` on the subtitle) so there is only **1 `<h1>` per chapter**.
2. **Prevent `<h1>` Explosion on TOC (`Contents`), Dedication & In-Story Headlines**:
   - If a line is a standalone chapter number word (`ONE`, `TWO` ... `THIRTY`) on a `Contents` page (or followed within `40px` by an `<a href="...">` TOC link), emit `<p class="toc-chapter-num"><strong>...</strong></p>` instead of `<h1>`.
   - On Dedication pages (e.g., Page 8 where 4 short centered lines have `font size=33`), or in-story signs (`The Ministry of Magic` on p.52, `Professor H. E. F. Slughorn` on p.143 which occur *after* the chapter's opening paragraph), downgrade any `BlockHeading1` that appears *after* the first `BlockParagraph` on a page to `<h2>` or `<h3>`.
3. **Colophon Double-Spaced Italic Paragraph (Page 667)**:
   - In [`collectParagraphLines`](file:///Users/sunday/Dev/Personal/Projects/rebind/internal/converter/pdf_statemachine.go#L875-L881): allow `pitch <= 2.4*config.MedianPitch` when both `prev.IsItalic && curr.IsItalic` and `math.Abs(prev.FontSize - curr.FontSize) <= 1.5`, so Page 667's centered double-spaced colophon (`pitch=32px`) stays together as one cohesive paragraph instead of splitting after `"directed by David Saylor."`.

---

## 5. Step-by-Step Verification Checklist for the Implementing Agent

Run these exact commands in order and verify the output at each gate:

```bash
# 1. After Phase 1: Run existing unit tests
go test -v ./...

# 2. Test fast page-level X-ray inspection (<200ms)
make inspect PAGES=15-16
make inspect PAGES=611-625
make inspect PAGES=667-667

# 3. Run baseline audit on existing sample files (expect to see all 5 baseline defects reported)
go run ./cmd/verify audit \
  --pdf sample/harry-potter-original-pdf.pdf \
  --epub sample/harry-potter-rebind.epub \
  --calibre sample/harry-potter-calibre.epub

# 4. After Phase 2 fixes: Re-convert sample PDF headlessly and run full verification gate
make verify
go run ./cmd/verify audit \
  --pdf sample/harry-potter-original-pdf.pdf \
  --epub sample/harry-potter-rebind.epub \
  --calibre sample/harry-potter-calibre.epub \
  --strict

# 5. Run full Go test suite (including new regression tests for 3-line drop caps, cross-page fusion, and sibling outlines)
go test -v ./...
```

### Target Acceptance Criteria on `sample/harry-potter-rebind.epub`
- **XML & Link Validity**: `0` XML syntax errors, `0` broken `<a href>` links/anchors, `0` broken `<img src>` paths, `0` escaped raw HTML tags.
- **Chapter Openings (Drop Caps)**: `0 / 30` scrambled chapter openings (down from `30 / 30`). All 30 chapters begin cleanly with `<p><span class="dropcap">...</span>...</p>`.
- **Cross-Page Paragraph Reflow**: `0` mid-sentence cross-page `<p>` breaks (down from `264`).
- **Chapter Count & TOC**: `33` chapters (`Copyright`, `Contents`, Chapters 1–30, `Credits`), `0` `"vii"` ghost chapter at the end, `0` `· <page>` suffixes in TOC labels.
- **Images**: Cover image set (`book-2_1.jpg`), all 30 chapter illustrations + colophon/copyright logos embedded (~33–35 images total), and Page 7's 231 micro-strips collapsed into `1` clean composite image (`0` micro-slice spam vs Calibre's `230`).
