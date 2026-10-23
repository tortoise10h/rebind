---
name: pdf-verify
description: Verify, inspect, and audit PDF-to-EPUB conversion quality in rebind. Use whenever checking conversion outputs, auditing EPUB quality against PDF/Calibre baselines, inspecting page-level layout pipeline stages (pdftohtml XML, visual lines, parsed blocks, XHTML), debugging chapter drop caps, TOC hierarchy, mid-sentence cross-page splits, or image/cover fidelity, or running `make verify` / `cmd/verify`.
---

# PDF-to-EPUB Verification & Quality Audit Skill

This skill guides agents on how to verify, inspect, and audit PDF-to-EPUB conversion quality in `rebind` using the non-blocking verification CLI (`cmd/verify`) and headless conversion flags.

---

## 1. Quick Verification Commands

### Full Audit Against Calibre Baseline
To convert the sample PDF headlessly and run the 6-stage deterministic audit:

```bash
# 1. Build rebind binary
go build -o bin/rebind .

# 2. Convert PDF headlessly in <4 seconds
./bin/rebind --headless -o sample/harry-potter-rebind.epub sample/harry-potter-original-pdf.pdf

# 3. Run audit against Calibre baseline
go run ./cmd/verify audit \
  --pdf sample/harry-potter-original-pdf.pdf \
  --epub sample/harry-potter-rebind.epub \
  --calibre sample/harry-potter-calibre.epub

# Strict gate (fails with exit code 1 if any critical defect found):
go run ./cmd/verify audit --strict
```

### Fast Page-Level Pipeline X-Ray (<150ms)
To inspect spatial layout, noise stripping, and block parsing for specific pages:

```bash
# Inspect Chapter 1 opening (page 15)
go run ./cmd/verify inspect --pdf sample/harry-potter-original-pdf.pdf --pages 15-15 --stage blocks

# Inspect specific pipeline stage (all, xml, lines, blocks, xhtml)
go run ./cmd/verify inspect --pdf sample/harry-potter-original-pdf.pdf --pages 15-16 --stage lines
go run ./cmd/verify inspect --pdf sample/harry-potter-original-pdf.pdf --pages 7-7 --stage xml
go run ./cmd/verify inspect --pdf sample/harry-potter-original-pdf.pdf --pages 611-611 --stage xhtml

# Machine-readable JSON output
go run ./cmd/verify inspect --pdf sample/harry-potter-original-pdf.pdf --pages 15-15 --json
```

---

## 2. The 6 Deterministic Quality Checks

The audit tool (`go run ./cmd/verify audit`) verifies 6 quality dimensions:

| Metric | Target Acceptance Criteria | What It Checks |
| :--- | :--- | :--- |
| **1. XML Validity Errors** | `0` errors | Strict XML parsing of all container, OPF, NCX, NAV, and XHTML files. |
| **2. Broken Links / Images** | `0` broken | Every `<a href="...">` and `<img src="...">` resolves to an existing file/anchor in the EPUB ZIP. No escaped tags (`&lt;a `, `&lt;p&gt;`). |
| **3. Drop-Cap Opening Sentences** | `0` scrambled | Chapter 1st sentences start with `<span class="dropcap">X</span>` without word fragmentation, stray spaces, or trailing scrambled lines. |
| **4. Mid-Sentence Page Breaks** | `0` breaks | Sentences wrapping across page anchors (`<span id="pN"></span>`) do not close `<p>` without sentence terminal punctuation (`.`, `!`, `?`, `”`, `…`, `—`). |
| **5. Clean Heading & TOC** | `0` multi-H1 files, `0` TOC noise | At most one `<h1>` per chapter file. Bookmark titles stripped of print page numbers (`· 1`) and sibling Roman numerals (`vii..x`). |
| **6. Images & Micro-Slice Spam** | Cover OK, `0` micro-slices | Cover image set (`meta name="cover"`). Multi-strip raster slices (e.g. Page 7's 231 strips) coalesced into 1 composite crop. |

---

## 3. Fast Headless Conversion Recipes

Agents should NEVER launch the interactive TUI during automated development. Use these flags:

```bash
# Full book conversion
./bin/rebind --headless -o output.epub input.pdf

# Test specific page range in <200ms
./bin/rebind --headless --pages 15-16 -o test_p15_16.epub input.pdf

# Force specific book profile (auto, fiction, technical)
./bin/rebind --headless --profile fiction -o output.epub input.pdf
```

---

## 4. Diagnostic & Troubleshooting Playbook

### Defect 1: Scrambled Chapter Drop Caps
- **Symptom**: `cmd/verify audit` reports `Scrambled drop cap! P1 starts with "I through his brain...", but P2 begins with "hind. He was waiting..."`.
- **Root Cause**: In `BuildVisualLines` (`pdf_layout.go`), Line 3 of body text shares bottom baseline with the 3-line drop cap `I` and was merged into the drop cap cluster.
- **Fix**: Guard baseline clustering with `maxH >= 1.8*minH && topDiff > 0.6*minH` so multi-line drop caps remain on their own line. In `renderParagraphBlock` (`pdf_statemachine.go`), call `fuseParagraphLines(lines[1:], config.Lexicon)` to de-hyphenate and join `<span class="dropcap">%c</span>` directly with the continuation word.

### Defect 2: Missing Images & 0 Images in ZIP
- **Symptom**: `cmd/verify audit` reports `0 images (No Cover)`.
- **Root Cause**: `pdftohtml` produces XML with absolute paths (`/var/folders/.../book-1_1.png`). Calling `filepath.Join(tmpDir, src)` doubles the temp dir path so `os.Stat` fails.
- **Fix**: Use `resolvePopplerImagePath(tmpDir, src)` in `addImage` and cover detection in `pdf_converter.go`.

### Defect 3: Micro-Slice Image Spam (e.g. 231 Slices on Page 7)
- **Symptom**: ZIP contains hundreds of 1px-tall image files (`book-7_1.png` .. `book-7_233.png`).
- **Root Cause**: Artwork sliced into 1px horizontal raster strips in the PDF source.
- **Fix**: In `parseSinglePage` (`pdf_statemachine.go`), detect vertically contiguous micro-strips (`group >= 4`, `height <= 8px`) and coalesce them into a single `CropRegion` call via `pdftoppm`.

### Defect 4: Mid-Sentence Cross-Page Paragraph Splits
- **Symptom**: Sentence broken across page boundary: `<p>...How on earth</p><div id="p16"></div><p>was his government...</p>`.
- **Root Cause**: Independent page-by-page parsing emits separate `<p>` tags for sentences wrapping across pages.
- **Fix**: Call `mergeCrossPageParagraphs` in `finalizeChapters` (`pdf_converter.go`) to fuse `<p>` blocks when `!prevEndsSentence || currStartsLower` and embed `<span id="p%d"></span>` at the join boundary.

### Defect 5: Ghost Chapters & TOC Page Suffixes
- **Symptom**: Bookmark titles retain print suffixes like `The Other Minister · 1` or append `chapter_034` titled `vii`.
- **Root Cause**: Poppler outline XML unmarshals nested Roman numeral sub-outlines (`vii..x`) after the main chapters.
- **Fix**: In `xmlOutline.Flatten` (`pdf_poppler.go`), strip `\s*[·•]\s*\d+\s*$` and filter out Roman numerals matching `^[ivxlcdm]+$`.

---

## 5. Verification Checklist for Code Changes

Before completing any change to `internal/converter` or `cmd/`:
1. Run unit tests: `go test -v -run 'TestPDF|TestFiction|TestTechnical|TestBuild|TestSplit|TestLigature|TestDetect|TestMultiLine|TestFlatten|TestMerge' ./...`
2. Run inspect on key chapters:
   - Page 15 (Chapter 1 opening): `go run ./cmd/verify inspect --pdf sample/harry-potter-original-pdf.pdf --pages 15-15 --stage blocks`
   - Page 611 (Flight of the Prince drop cap + dialogue): `go run ./cmd/verify inspect --pdf sample/harry-potter-original-pdf.pdf --pages 611-611 --stage blocks`
3. Run strict audit: `go run ./cmd/verify audit --strict`
