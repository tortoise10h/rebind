package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDetectInputKind(t *testing.T) {
	// 1. Web URL
	kind, target, err := DetectInputKind("https://example.com/blog/article")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != InputWebURL || target != "https://example.com/blog/article" {
		t.Errorf("expected InputWebURL, got %v (%s)", kind, target)
	}

	// 2. Bare domain URL
	kind, target, err = DetectInputKind("example.com/blog/article")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != InputWebURL || target != "https://example.com/blog/article" {
		t.Errorf("expected InputWebURL, got %v (%s)", kind, target)
	}

	// 3. Remote PDF URL
	kind, target, err = DetectInputKind("https://example.com/docs/manual.pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != InputRemotePDF || target != "https://example.com/docs/manual.pdf" {
		t.Errorf("expected InputRemotePDF, got %v (%s)", kind, target)
	}

	// 4. Local PDF File (create temporary file with spaces)
	tmpDir := t.TempDir()
	pdfFile := filepath.Join(tmpDir, "My Great Book.pdf")
	if err := os.WriteFile(pdfFile, []byte("%PDF-1.4\n%test\n"), 0o644); err != nil {
		t.Fatalf("create temp pdf: %v", err)
	}

	// 4a. Exact path
	kind, target, err = DetectInputKind(pdfFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != InputLocalPDF || target != pdfFile {
		t.Errorf("expected InputLocalPDF, got %v (%s)", kind, target)
	}

	// 4b. Single-quoted drag & drop path
	kind, target, err = DetectInputKind("'" + pdfFile + "'")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != InputLocalPDF || target != pdfFile {
		t.Errorf("expected InputLocalPDF from quoted, got %v (%s)", kind, target)
	}

	// 4c. Finder backslash-escaped drag & drop path: /path/to/My\ Great\ Book.pdf
	escapedPath := strings.ReplaceAll(pdfFile, " ", `\ `)
	kind, target, err = DetectInputKind(escapedPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != InputLocalPDF || target != pdfFile {
		t.Errorf("expected InputLocalPDF from escaped path, got %v (%s)", kind, target)
	}

	// 4d. file:// URL
	fileURL := "file://" + pdfFile
	kind, target, err = DetectInputKind(fileURL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != InputLocalPDF || target != pdfFile {
		t.Errorf("expected InputLocalPDF from file://, got %v (%s)", kind, target)
	}

	// 5. Non-existent PDF file
	nonExistent := filepath.Join(tmpDir, "missing.pdf")
	_, _, err = DetectInputKind(nonExistent)
	if err == nil || !strings.Contains(err.Error(), "PDF file not found") {
		t.Errorf("expected 'PDF file not found' error, got %v", err)
	}
}

func TestLigatureAndFontClassification(t *testing.T) {
	// Ligature expansion: ff, fi, fl, ffi, ffl, st, st
	input := "o\uFB00ice \uFB01rst \uFB02oor e\uFB03cient a\uFB04iction \uFB05yle po\uFB06"
	expected := "office first floor efficient affliction style post"
	expanded := expandUnicodeLigatures(input)
	if expanded != expected {
		t.Errorf("ligature expansion mismatch:\ngot:  %q\nwant: %q", expanded, expected)
	}

	// Monospace classification with subset prefix stripping
	spec1 := xmlFontSpec{
		ID:     "1",
		Family: "FAAAAA+Menlo-Regular",
		Size:   12,
	}
	info1 := classifyFont(spec1)
	if info1.Family != "Menlo-Regular" {
		t.Errorf("expected Menlo-Regular, got %s", info1.Family)
	}
	if !info1.IsMonospace {
		t.Errorf("expected IsMonospace == true for Menlo")
	}

	// Bold & Italic classification
	spec2 := xmlFontSpec{
		ID:     "2",
		Family: "TimesNewRoman-BoldItalic",
		Size:   14,
	}
	info2 := classifyFont(spec2)
	if !info2.IsBold || !info2.IsItalic || info2.IsMonospace {
		t.Errorf("expected bold & italic, got bold=%v italic=%v mono=%v", info2.IsBold, info2.IsItalic, info2.IsMonospace)
	}
}

func TestBuildVisualLinesAndStripRunningNoise(t *testing.T) {
	// Construct synthetic page with inline code coalescing
	fontMap := map[string]FontInfo{
		"f0": {ID: "f0", Family: "Helvetica", Size: 12, IsMonospace: false},
		"f1": {ID: "f1", Family: "Menlo", Size: 12, IsMonospace: true},
	}

	page := &xmlPage{
		Number: 1,
		Width:  600,
		Height: 800,
		Texts: []xmlText{
			{Top: 100, Left: 50, Width: 80, Height: 12, FontID: "f0", InnerXML: "Call the "},
			{Top: 100, Left: 135, Width: 45, Height: 12, FontID: "f1", InnerXML: "run()"},
			{Top: 100, Left: 185, Width: 120, Height: 12, FontID: "f0", InnerXML: " method directly."},
		},
	}

	lines := BuildVisualLines(page, fontMap)
	if len(lines) != 1 {
		t.Fatalf("expected 1 coalesced visual line, got %d", len(lines))
	}
	l := lines[0]
	if !strings.Contains(l.RichXHTML, "<code>run()</code>") {
		t.Errorf("expected inline code tag in RichXHTML, got %s", l.RichXHTML)
	}
	if l.PlainText != "Call the run() method directly." {
		t.Errorf("unexpected plain text: %q", l.PlainText)
	}

	// Cross-page noise removal test across 3 pages
	var p1, p2, p3 []VisualLine
	// Header zone (< 0.085 * 1000 = 85)
	headerText := "A Journey Through Code"
	p1 = append(p1, VisualLine{Top: 40, Height: 12, PlainText: headerText})
	p2 = append(p2, VisualLine{Top: 40, Height: 12, PlainText: headerText})
	p3 = append(p3, VisualLine{Top: 40, Height: 12, PlainText: headerText})

	// Body text
	p1 = append(p1, VisualLine{Top: 200, Height: 14, FontSize: 12, PlainText: "Page 1 content body paragraph."})
	p2 = append(p2, VisualLine{Top: 200, Height: 14, FontSize: 12, PlainText: "Page 2 content body paragraph."})
	p3 = append(p3, VisualLine{Top: 200, Height: 14, FontSize: 12, PlainText: "Page 3 content body paragraph."})

	// Footer zone (> 0.915 * 1000 = 915)
	p1 = append(p1, VisualLine{Top: 950, Height: 12, PlainText: "1"})
	p2 = append(p2, VisualLine{Top: 950, Height: 12, PlainText: "2"})
	p3 = append(p3, VisualLine{Top: 950, Height: 12, PlainText: "3"})

	cleaned := StripRunningNoise([][]VisualLine{p1, p2, p3}, []float64{1000, 1000, 1000})

	for i, pageLines := range cleaned {
		if len(pageLines) != 1 {
			t.Errorf("page %d: expected 1 line after noise removal, got %d", i+1, len(pageLines))
		} else if !strings.Contains(pageLines[0].PlainText, "body paragraph") {
			t.Errorf("page %d: wrong line kept: %s", i+1, pageLines[0].PlainText)
		}
	}
}

func TestSplitPageColumns(t *testing.T) {
	// Construct 2-column page layout
	pageW := 600.0
	var lines []VisualLine
	// Spanning title
	lines = append(lines, VisualLine{Top: 50, Left: 50, Width: 500, Height: 24, PlainText: "Spanning Document Title"})

	// Left column (left: 50, right: 280)
	lines = append(lines, VisualLine{Top: 120, Left: 50, Width: 230, Right: 280, Height: 14, PlainText: "Left 1"})
	lines = append(lines, VisualLine{Top: 140, Left: 50, Width: 230, Right: 280, Height: 14, PlainText: "Left 2"})
	lines = append(lines, VisualLine{Top: 160, Left: 50, Width: 230, Right: 280, Height: 14, PlainText: "Left 3"})
	lines = append(lines, VisualLine{Top: 180, Left: 50, Width: 230, Right: 280, Height: 14, PlainText: "Left 4"})

	// Right column (left: 320, right: 550)
	lines = append(lines, VisualLine{Top: 120, Left: 320, Width: 230, Right: 550, Height: 14, PlainText: "Right 1"})
	lines = append(lines, VisualLine{Top: 140, Left: 320, Width: 230, Right: 550, Height: 14, PlainText: "Right 2"})
	lines = append(lines, VisualLine{Top: 160, Left: 320, Width: 230, Right: 550, Height: 14, PlainText: "Right 3"})
	lines = append(lines, VisualLine{Top: 180, Left: 320, Width: 230, Right: 550, Height: 14, PlainText: "Right 4"})

	ordered := SplitPageColumns(lines, pageW)
	if len(ordered) != 9 {
		t.Fatalf("expected 9 lines, got %d", len(ordered))
	}

	if ordered[0].PlainText != "Spanning Document Title" {
		t.Errorf("expected spanning title first, got %s", ordered[0].PlainText)
	}
	if ordered[1].PlainText != "Left 1" || ordered[4].PlainText != "Left 4" {
		t.Errorf("expected left column lines first, got %s ... %s", ordered[1].PlainText, ordered[4].PlainText)
	}
	if ordered[5].PlainText != "Right 1" || ordered[8].PlainText != "Right 4" {
		t.Errorf("expected right column lines second, got %s ... %s", ordered[5].PlainText, ordered[8].PlainText)
	}
}

func TestFictionStateMachine(t *testing.T) {
	// Test Scene Break
	if !isSceneBreakLine("* * *") {
		t.Errorf("expected * * * to be scene break")
	}
	if !isSceneBreakLine("— — —") {
		t.Errorf("expected — — — to be scene break")
	}

	// Test Lexicon De-hyphenation
	lex := &DocumentLexicon{
		unsplitWords: map[string]int{
			"reflowable": 5,
		},
		hyphenatedWords: map[string]int{
			"well-known": 4,
		},
	}

	// Should keep hyphen for well-known
	r1 := lex.Dehyphenate("well-", "known")
	if r1 != "well-known" {
		t.Errorf("expected well-known, got %s", r1)
	}

	// Should strip hyphen for reflowable
	r2 := lex.Dehyphenate("re-", "flowable")
	if r2 != "reflowable" {
		t.Errorf("expected reflowable, got %s", r2)
	}

	// Test Dialogue line preservation
	lines := []VisualLine{
		{Top: 100, Left: 50, Width: 450, Right: 500, Height: 14, FontSize: 12, PageWidth: 600, PlainText: "He stood in silence watching the dawn emerge."},
		{Top: 116, Left: 50, Width: 350, Right: 400, Height: 14, FontSize: 12, PageWidth: 600, PlainText: "“We have to leave now,” she whispered urgently."},
	}

	cfg := StateMachineConfig{
		Profile:      ProfileFiction,
		MedianPitch:  16,
		MedianHeight: 12,
		Lexicon:      lex,
	}

	blocks := ParsePDFPages(nil, [][]VisualLine{lines}, cfg)
	if len(blocks) != 2 {
		t.Fatalf("expected dialogue line to start fresh paragraph (2 blocks), got %d blocks", len(blocks))
	}
	if !strings.Contains(blocks[1].XHTML, "“We have to leave now") {
		t.Errorf("expected dialogue in second block, got %s", blocks[1].XHTML)
	}

	// Test Drop Cap: single large uppercase letter line + paragraph continuation
	dropCapDocLines := []VisualLine{
		{Top: 100, Left: 50, Width: 25, Height: 30, FontSize: 28, PageWidth: 600, PlainText: "O"},
		{Top: 108, Left: 80, Width: 420, Height: 14, FontSize: 12, PageWidth: 600, PlainText: "nce upon a time in a faraway land there lived a wise storyteller."},
		{Top: 126, Left: 50, Width: 450, Height: 14, FontSize: 12, PageWidth: 600, PlainText: "He traveled across mountains and plains sharing tales of wonder with everyone."},
	}
	dropBlocks := ParsePDFPages(nil, [][]VisualLine{dropCapDocLines}, cfg)
	if len(dropBlocks) == 0 {
		t.Fatalf("expected blocks for dropcap doc, got 0")
	}
	if !strings.Contains(dropBlocks[0].XHTML, `<span class="dropcap">O</span>nce`) {
		t.Errorf("expected dropcap span in first block, got %s", dropBlocks[0].XHTML)
	}
}

func TestTechnicalStateMachine(t *testing.T) {
	// 1. Monospace and box drawing block
	codeLines := []VisualLine{
		{Top: 100, Left: 80, Width: 200, Height: 14, FontSize: 12, IsMonospace: true, PlainText: "┌─────────┐"},
		{Top: 116, Left: 80, Width: 200, Height: 14, FontSize: 12, IsMonospace: true, PlainText: "│  State  │"},
		{Top: 132, Left: 80, Width: 200, Height: 14, FontSize: 12, IsMonospace: true, PlainText: "└─────────┘"},
	}

	cfg := StateMachineConfig{
		Profile:      ProfileTechnical,
		MedianPitch:  16,
		MedianHeight: 12,
	}

	blocks := ParsePDFPages(nil, [][]VisualLine{codeLines}, cfg)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 code block, got %d", len(blocks))
	}
	if blocks[0].Kind != BlockCode {
		t.Errorf("expected BlockCode, got %v", blocks[0].Kind)
	}
	if !strings.Contains(blocks[0].XHTML, "<pre><code>") || !strings.Contains(blocks[0].XHTML, "│  State  │") {
		t.Errorf("expected box drawing diagram in pre/code, got %s", blocks[0].XHTML)
	}

	// 2. Simple Table (2 columns)
	tableLines := []VisualLine{
		{
			Top: 200, Left: 50, Width: 400, Height: 14, HasMultipleColumns: true, IsBold: true,
			PlainText: "Header 1   Header 2",
			CellStops: []TextCell{
				{Left: 50, Right: 200, PlainText: "Header 1"},
				{Left: 250, Right: 450, PlainText: "Header 2"},
			},
		},
		{
			Top: 220, Left: 50, Width: 400, Height: 14, HasMultipleColumns: true,
			PlainText: "Data A   Data B",
			CellStops: []TextCell{
				{Left: 50, Right: 200, PlainText: "Data A"},
				{Left: 250, Right: 450, PlainText: "Data B"},
			},
		},
	}

	tBlocks := ParsePDFPages(nil, [][]VisualLine{tableLines}, cfg)
	if len(tBlocks) != 1 || tBlocks[0].Kind != BlockTable {
		t.Fatalf("expected 1 BlockTable, got %d", len(tBlocks))
	}
	if !strings.Contains(tBlocks[0].XHTML, "<table>") || !strings.Contains(tBlocks[0].XHTML, "<th>Header 1</th>") {
		t.Errorf("expected table HTML with th, got %s", tBlocks[0].XHTML)
	}

	// 3. Complex Table (> 3 columns) triggers region cropper callback
	cropperCalled := false
	complexTableLines := []VisualLine{
		{
			Top: 300, Left: 50, Width: 500, Height: 14, HasMultipleColumns: true,
			PlainText: "C1 C2 C3 C4 C5",
			CellStops: []TextCell{
				{Left: 50, Right: 120, PlainText: "C1"},
				{Left: 140, Right: 210, PlainText: "C2"},
				{Left: 230, Right: 300, PlainText: "C3"},
				{Left: 320, Right: 390, PlainText: "C4"},
				{Left: 410, Right: 480, PlainText: "C5"},
			},
		},
		{
			Top: 320, Left: 50, Width: 500, Height: 14, HasMultipleColumns: true,
			PlainText: "D1 D2 D3 D4 D5",
			CellStops: []TextCell{
				{Left: 50, Right: 120, PlainText: "D1"},
				{Left: 140, Right: 210, PlainText: "D2"},
				{Left: 230, Right: 300, PlainText: "D3"},
				{Left: 320, Right: 390, PlainText: "D4"},
				{Left: 410, Right: 480, PlainText: "D5"},
			},
		},
	}

	cfgWithCrop := StateMachineConfig{
		Profile:      ProfileTechnical,
		MedianPitch:  16,
		MedianHeight: 12,
		CropRegion: func(pageNum int, left, top, width, height float64, label string) (string, error) {
			cropperCalled = true
			return "../images/table_cropped.png", nil
		},
	}

	cBlocks := ParsePDFPages(nil, [][]VisualLine{complexTableLines}, cfgWithCrop)
	if !cropperCalled {
		t.Errorf("expected region cropper to be called for > 3 columns table")
	}
	if len(cBlocks) != 1 || !strings.Contains(cBlocks[0].XHTML, "table_cropped.png") {
		t.Errorf("expected cropped image figure in block, got %s", cBlocks[0].XHTML)
	}
}

func TestPDFEndToEndWithPoppler(t *testing.T) {
	// Skip if poppler tools are not installed
	if _, err := exec.LookPath("pdftohtml"); err != nil {
		if _, err := os.Stat("/opt/homebrew/bin/pdftohtml"); err != nil {
			t.Skip("pdftohtml not available on PATH, skipping integration test")
		}
	}

	// Create a minimal 2-page valid PDF
	pdfBytes := []byte(`%PDF-1.4
1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj
2 0 obj << /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 >> endobj
3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >> endobj
4 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj
5 0 obj << /Length 85 >> stream
BT
/F1 24 Tf
100 700 Td
(Chapter 1: The Beginning) Tj
/F1 12 Tf
0 -40 Td
(This is the opening chapter of the story.) Tj
ET
endstream
endobj
6 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 7 0 R >> endobj
7 0 obj << /Length 87 >> stream
BT
/F1 24 Tf
100 700 Td
(Chapter 2: The Journey) Tj
/F1 12 Tf
0 -40 Td
(They traveled across the winding hills.) Tj
ET
endstream
endobj
xref
0 8
0000000000 65535 f 
0000000009 00000 n 
0000000058 00000 n 
0000000122 00000 n 
0000000231 00000 n 
0000000300 00000 n 
0000000436 00000 n 
0000000545 00000 n 
trailer << /Size 8 /Root 1 0 R >>
startxref
683
%%EOF
`)

	tmpDir := t.TempDir()
	pdfPath := filepath.Join(tmpDir, "test_book.pdf")
	if err := os.WriteFile(pdfPath, pdfBytes, 0o644); err != nil {
		t.Fatalf("write fixture pdf: %v", err)
	}

	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir outDir: %v", err)
	}

	res, err := ConvertWithOptions(context.Background(), pdfPath, ConvertOptions{
		OutputDir: outDir,
		Profile:   ProfileAuto,
	}, nil)
	if err != nil {
		t.Fatalf("ConvertWithOptions failed: %v", err)
	}

	if res.PagesTotal != 2 {
		t.Errorf("expected 2 pages total, got %d", res.PagesTotal)
	}
	if res.Chapters < 2 {
		t.Errorf("expected at least 2 chapters, got %d", res.Chapters)
	}
	if _, err := os.Stat(res.OutputPath); err != nil {
		t.Fatalf("output EPUB not found: %v", err)
	}

	// Open the generated EPUB (zip archive) and verify strict XML validity of every chapter file
	zr, err := zip.OpenReader(res.OutputPath)
	if err != nil {
		t.Fatalf("open epub zip: %v", err)
	}
	defer zr.Close()

	chaptersFound := 0
	foundCSS := false

	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, ".xhtml") {
			chaptersFound++
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open %s: %v", f.Name, err)
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatalf("read %s: %v", f.Name, err)
			}

			// Parse strictly with encoding/xml
			dec := xml.NewDecoder(bytes.NewReader(data))
			for {
				if _, err := dec.Token(); err != nil {
					if err == io.EOF {
						break
					}
					t.Fatalf("strict XML validation failed on %s: %v\nContent:\n%s", f.Name, err, string(data))
				}
			}
		}
		if strings.Contains(f.Name, "style.css") {
			foundCSS = true
		}
	}

	if chaptersFound < 2 {
		t.Errorf("expected >= 2 xhtml chapters in EPUB, got %d", chaptersFound)
	}
	if !foundCSS {
		t.Errorf("expected style.css in EPUB archive")
	}
}

func TestParseInnerXMLAndLinkRewriting(t *testing.T) {
	// 1. Raw <a> tag parsing from Poppler (User bug report: raw <a> rendered on screen)
	rawXML := `<a href="book.html#208">The House of Gaunt &#183; 194 </a>`
	font := FontInfo{ID: "f0", Size: 12}
	plain, rich, isBold, isItalic := parseInnerXML(rawXML, font)

	if plain != "The House of Gaunt · 194 " {
		t.Errorf("expected clean plain text without tags, got %q", plain)
	}
	if strings.Contains(plain, "<a") || strings.Contains(plain, "</a>") {
		t.Errorf("plain text must not contain raw HTML tags, got %q", plain)
	}
	if strings.Contains(rich, "&lt;a") || strings.Contains(rich, "&gt;") {
		t.Errorf("richXHTML must not contain escaped raw HTML tags (&lt;a...), got %q", rich)
	}
	if !strings.Contains(rich, `<a href="book.html#208">`) {
		t.Errorf("richXHTML should contain valid <a> tag, got %q", rich)
	}
	if isBold || isItalic {
		t.Errorf("expected isBold=false, isItalic=false, got bold=%v italic=%v", isBold, isItalic)
	}

	// 2. Bold and italic inline markup (User bug report: raw <i> rendered on screen)
	formatXML := `<b>Bold Chapter</b> and <i>The Daily Prophet</i> in italics`
	plainFmt, richFmt, boldFmt, italicFmt := parseInnerXML(formatXML, font)
	if plainFmt != "Bold Chapter and The Daily Prophet in italics" {
		t.Errorf("unexpected plain text: %q", plainFmt)
	}
	if strings.Contains(plainFmt, "<i") || strings.Contains(plainFmt, "</i>") {
		t.Errorf("plain text must not contain raw <i> tags, got %q", plainFmt)
	}
	if strings.Contains(richFmt, "&lt;i") || strings.Contains(richFmt, "&lt;/i") {
		t.Errorf("richXHTML must not contain escaped raw <i> tags, got %q", richFmt)
	}
	if !strings.Contains(richFmt, "<em>The Daily Prophet</em>") {
		t.Errorf("richXHTML should contain italic markup for <i>, got %q", richFmt)
	}
	if !boldFmt || !italicFmt {
		t.Errorf("expected bold=true and italic=true, got bold=%v italic=%v", boldFmt, italicFmt)
	}

	// 2b. Nested <a> and <i> tags
	nestedXML := `<a href="book.html#208"><i>Chapter 1</i></a>`
	plainNest, richNest, _, _ := parseInnerXML(nestedXML, font)
	if plainNest != "Chapter 1" {
		t.Errorf("expected clean text from nested tags, got %q", plainNest)
	}
	if strings.Contains(richNest, "&lt;") {
		t.Errorf("richXHTML must not contain escaped tags, got %q", richNest)
	}
	if !strings.Contains(richNest, `<a href="book.html#208"><em>Chapter 1</em></a>`) {
		t.Errorf("expected nested a and em tags, got %q", richNest)
	}

	// 3. Resilience on unclosed HTML (auto-repaired by parser to valid XML)
	brokenXML := `<a href="broken">Unclosed tag`
	plainBroken, richBroken, _, _ := parseInnerXML(brokenXML, font)
	if plainBroken != "Unclosed tag" {
		t.Errorf("plain text should extract text without tags, got %q", plainBroken)
	}
	if richBroken != `<a href="broken">Unclosed tag</a>` {
		t.Errorf("expected auto-closed valid XML tag, got %q", richBroken)
	}
	stripped := stripHTMLTags("<a href=\"broken\">Unclosed tag")
	if stripped != "Unclosed tag" {
		t.Errorf("stripHTMLTags failed: got %q", stripped)
	}

	// 4. Link destination rewriting
	pageToChapter := map[int]string{
		208: "chapter_011.xhtml",
		231: "chapter_012.xhtml",
	}

	// 4a. Cross-chapter link
	inputXHTML := `<p><a href="book.html#208">The House of Gaunt · 194 </a></p>`
	rewritten := rewritePopplerLinks(inputXHTML, "chapter_001.xhtml", pageToChapter)
	expectedTarget := `<p><a href="chapter_011.xhtml#p208">The House of Gaunt · 194 </a></p>`
	if rewritten != expectedTarget {
		t.Errorf("expected cross-chapter link %q, got %q", expectedTarget, rewritten)
	}

	// 4b. Same-chapter link
	inputSame := `<p><a href="book.html#208">The House of Gaunt · 194 </a></p>`
	rewrittenSame := rewritePopplerLinks(inputSame, "chapter_011.xhtml", pageToChapter)
	expectedSame := `<p><a href="#p208">The House of Gaunt · 194 </a></p>`
	if rewrittenSame != expectedSame {
		t.Errorf("expected same-chapter link %q, got %q", expectedSame, rewrittenSame)
	}

	// 4c. Bare page link (#231)
	inputBare := `<p><a href="#231">Hermione's Helping Hand</a></p>`
	rewrittenBare := rewritePopplerLinks(inputBare, "chapter_001.xhtml", pageToChapter)
	expectedBare := `<p><a href="chapter_012.xhtml#p231">Hermione's Helping Hand</a></p>`
	if rewrittenBare != expectedBare {
		t.Errorf("expected bare link rewritten %q, got %q", expectedBare, rewrittenBare)
	}

	// 4d. Unresolvable phantom book.html link
	inputUnresolvable := `<p><a href="book.html#9999">Unknown Page</a></p>`
	rewrittenUnres := rewritePopplerLinks(inputUnresolvable, "chapter_001.xhtml", pageToChapter)
	expectedUnres := `<p>Unknown Page</p>`
	if rewrittenUnres != expectedUnres {
		t.Errorf("expected unresolvable phantom link unwrapped to %q, got %q", expectedUnres, rewrittenUnres)
	}

	// 4e. External HTTP link untouched
	inputExt := `<p><a href="https://example.com/source">External Link</a></p>`
	rewrittenExt := rewritePopplerLinks(inputExt, "chapter_001.xhtml", pageToChapter)
	if rewrittenExt != inputExt {
		t.Errorf("expected external link unchanged, got %q", rewrittenExt)
	}

	// 5. FinalizeChapters page anchors and validation
	blocks := []ParsedBlock{
		{Kind: BlockParagraph, PageNum: 1, XHTML: "<p>Page 1 Content</p>"},
		{Kind: BlockParagraph, PageNum: 2, XHTML: "<p>Page 2 Content</p>"},
	}
	finalChapters := finalizeChapters([]chapterData{
		{Title: "Chapter 1", Blocks: blocks},
	}, "Test Document")

	if len(finalChapters) != 1 {
		t.Fatalf("expected 1 chapter, got %d", len(finalChapters))
	}
	ch := finalChapters[0]
	if !strings.Contains(ch.XHTML, `id="p1"`) || !strings.Contains(ch.XHTML, `id="p2"`) {
		t.Errorf("expected page anchors for p1 and p2, got %s", ch.XHTML)
	}
	if err := validateXHTMLFragment(ch.XHTML); err != nil {
		t.Errorf("finalized chapter failed strict XHTML validation: %v", err)
	}
}

func TestMultiLineDropCapClustering(t *testing.T) {
	// Drop cap 'I' at top=434, height=94 (bottom=528)
	// Line 1: top=458, height=25 (bottom=483) "t was nearing midnight"
	// Line 2: top=483, height=25 (bottom=508) "alone in his office"
	// Line 3: top=508, height=25 (bottom=533) "through his brain without"
	p := &xmlPage{
		Number: 15,
		Width:  658,
		Height: 978,
		Texts: []xmlText{
			{Top: 434, Left: 72, Width: 32, Height: 94, FontID: "f1", InnerXML: "I"},
			{Top: 458, Left: 106, Width: 490, Height: 25, FontID: "f2", InnerXML: "t was nearing midnight"},
			{Top: 483, Left: 106, Width: 486, Height: 25, FontID: "f2", InnerXML: "alone in his office"},
			{Top: 508, Left: 72, Width: 515, Height: 25, FontID: "f2", InnerXML: "through his brain without"},
		},
	}
	fontMap := map[string]FontInfo{
		"f1": {ID: "f1", Size: 70, Family: "Able"},
		"f2": {ID: "f2", Size: 20, Family: "AGaramondPro"},
	}

	lines := BuildVisualLines(p, fontMap)
	if len(lines) != 4 {
		t.Fatalf("expected 4 visual lines (drop cap should not merge with line 3), got %d", len(lines))
	}
	if strings.TrimSpace(lines[0].PlainText) != "I" {
		t.Errorf("expected line 0 to be standalone drop cap 'I', got %q", lines[0].PlainText)
	}

	cfg := StateMachineConfig{
		Profile:      ProfileFiction,
		MedianPitch:  25.0,
		MedianHeight: 25.0,
	}
	blocks := parseSinglePage(lines, nil, 15, 20.0, cfg)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 merged paragraph block, got %d", len(blocks))
	}
	expectedPrefix := `<p><span class="dropcap">I</span>t was nearing midnight`
	if !strings.HasPrefix(blocks[0].XHTML, expectedPrefix) {
		t.Errorf("expected drop cap paragraph starting with %q, got %q", expectedPrefix, blocks[0].XHTML)
	}
}

func TestFlattenOutlineHygiene(t *testing.T) {
	out := &xmlOutline{
		Items: []xmlOutlineItem{
			{Page: 8, Title: "Copyright"},
			{Page: 9, Title: "Contents"},
			{Page: 15, Title: "The Other Minister · 1"},
			{Page: 667, Title: "Credits"},
		},
		Outlines: []xmlOutline{
			{
				Items: []xmlOutlineItem{
					{Page: 9, Title: "vii"},
					{Page: 10, Title: "viii"},
					{Page: 11, Title: "ix"},
					{Page: 12, Title: "x"},
				},
			},
		},
	}

	bookmarks := out.Flatten()
	if len(bookmarks) != 4 {
		t.Fatalf("expected 4 clean bookmarks (no ghost Roman numerals), got %d: %+v", len(bookmarks), bookmarks)
	}
	if bookmarks[2].Title != "The Other Minister" {
		t.Errorf("expected page suffix stripped from title, got %q", bookmarks[2].Title)
	}
	if bookmarks[3].Title != "Credits" {
		t.Errorf("expected last bookmark to be Credits, got %q", bookmarks[3].Title)
	}
}

func TestMergeCrossPageParagraphs(t *testing.T) {
	blocks := []ParsedBlock{
		{
			Kind:    BlockParagraph,
			PageNum: 15,
			XHTML:   "<p>accusations, for they were neither fair nor true. How on earth</p>",
		},
		{
			Kind:    BlockParagraph,
			PageNum: 16,
			XHTML:   "<p>was his government supposed to have stopped that bridge</p>",
		},
	}

	merged := mergeCrossPageParagraphs(blocks, nil)
	if len(merged) != 1 {
		t.Fatalf("expected 1 merged block across pages, got %d", len(merged))
	}
	expected := `<p>accusations, for they were neither fair nor true. How on earth <span id="p16"></span>was his government supposed to have stopped that bridge</p>`
	if merged[0].XHTML != expected {
		t.Errorf("expected merged XHTML:\n%s\ngot:\n%s", expected, merged[0].XHTML)
	}
}

func TestMergeCrossPageCodeBlocks(t *testing.T) {
	blocks := []ParsedBlock{
		{
			Kind:    BlockCode,
			PageNum: 169,
			XHTML:   "<pre><code>public class AndSpecification {\n   one = x;</code></pre>",
		},
		{
			Kind:    BlockCode,
			PageNum: 170,
			XHTML:   "<pre><code>   other = y;\n}</code></pre>",
		},
	}

	merged := mergeCrossPageParagraphs(blocks, nil)
	if len(merged) != 1 {
		t.Fatalf("expected 1 merged code block across pages, got %d", len(merged))
	}
	expected := "<pre><code>public class AndSpecification {\n   one = x;\n<span id=\"p170\"></span>   other = y;\n}</code></pre>"
	if merged[0].XHTML != expected {
		t.Errorf("expected merged XHTML:\n%s\ngot:\n%s", expected, merged[0].XHTML)
	}
}

func TestCodeBlockIndentation(t *testing.T) {
	codeLines := []VisualLine{
		{
			PlainText:   "public abstract class AbstractSpecification {",
			Left:        124.0,
			Top:         100.0,
			FontSize:    11.0,
			IsMonospace: true,
		},
		{
			PlainText:   "   public Specification and() {",
			Left:        124.0,
			Top:         113.0,
			FontSize:    11.0,
			IsMonospace: true,
		},
		{
			PlainText:   "      return new AndSpecification();",
			Left:        124.0,
			Top:         126.0,
			FontSize:    11.0,
			IsMonospace: true,
		},
		{
			PlainText:   "   }",
			Left:        124.0,
			Top:         139.0,
			FontSize:    11.0,
			IsMonospace: true,
		},
	}

	rendered := renderCodeBlock(codeLines)
	expectedLines := []string{
		"<pre><code>public abstract class AbstractSpecification {",
		"   public Specification and() {",
		"      return new AndSpecification();",
		"   }</code></pre>",
	}
	for _, exp := range expectedLines {
		if !strings.Contains(rendered, exp) {
			t.Errorf("expected rendered code block to contain %q, but got:\n%s", exp, rendered)
		}
	}
}

func TestDropCapNoInitialFalsePositive(t *testing.T) {
	// A single-line byline with large initials like "J. K. Rowling" must NOT be wrapped in <span class="dropcap">
	lines := []VisualLine{
		{
			PlainText: "J. K. Rowling",
			FontSize:  60.0,
			Top:       571.0,
			Height:    42.0,
			Right:     300.0,
			PageWidth: 658.0,
		},
	}
	cfg := StateMachineConfig{
		Profile:      ProfileFiction,
		MedianPitch:  25.0,
		MedianHeight: 25.0,
	}
	blocks := parseSinglePage(lines, nil, 7, 20.0, cfg)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if strings.Contains(blocks[0].XHTML, "dropcap") {
		t.Errorf("author initials 'J. K. Rowling' should not contain dropcap, got %s", blocks[0].XHTML)
	}
}

func TestPostProcessEPUBHygiene(t *testing.T) {
	// Verify that PostProcessEPUB successfully fixes NCX metadata and OPF guide
	epubPath := "../../sample/harry-potter-rebind.epub"
	if _, err := os.Stat(epubPath); err != nil {
		t.Skip("sample epub not found, skipping test")
	}

	zr, err := zip.OpenReader(epubPath)
	if err != nil {
		t.Fatalf("failed to open epub: %v", err)
	}
	defer zr.Close()

	var hasUID, hasIntDepth, hasGuide, hasLandmarks bool
	var missingPlayOrder int

	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "toc.ncx") {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			s := string(data)

			if strings.Contains(s, `name="dtb:uid"`) {
				hasUID = true
			}
			if strings.Contains(s, `name="dtb:depth" content="1"`) {
				hasIntDepth = true
			}
			matches := regexp.MustCompile(`(?i)<navPoint\b[^>]*>`).FindAllString(s, -1)
			for _, m := range matches {
				if !strings.Contains(strings.ToLower(m), "playorder=") {
					missingPlayOrder++
				}
			}
		}
		if strings.HasSuffix(f.Name, ".opf") {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			if strings.Contains(string(data), "<guide>") {
				hasGuide = true
			}
		}
		if strings.HasSuffix(f.Name, "nav.xhtml") {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			if strings.Contains(string(data), `epub:type="landmarks"`) {
				hasLandmarks = true
			}
		}
	}

	if !hasUID {
		t.Errorf("expected toc.ncx to have dtb:uid meta tag")
	}
	if !hasIntDepth {
		t.Errorf("expected toc.ncx to have integer dtb:depth=1")
	}
	if missingPlayOrder > 0 {
		t.Errorf("expected 0 navPoints missing playOrder, found %d", missingPlayOrder)
	}
	if !hasGuide {
		t.Errorf("expected OPF to have <guide> section for TOC")
	}
	if !hasLandmarks {
		t.Errorf("expected nav.xhtml to have landmarks section")
	}
}
