package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"rebind/internal/converter"
)

func parsePageRange(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	parts := strings.Split(s, "-")
	if len(parts) == 1 {
		p, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || p <= 0 {
			return 0, 0, fmt.Errorf("invalid page number: %s", s)
		}
		return p, p, nil
	}
	if len(parts) == 2 {
		firstStr := strings.TrimSpace(parts[0])
		lastStr := strings.TrimSpace(parts[1])
		first, last := 0, 0
		var err error
		if firstStr != "" {
			first, err = strconv.Atoi(firstStr)
			if err != nil || first <= 0 {
				return 0, 0, fmt.Errorf("invalid start page: %s", firstStr)
			}
		}
		if lastStr != "" {
			last, err = strconv.Atoi(lastStr)
			if err != nil || last <= 0 {
				return 0, 0, fmt.Errorf("invalid end page: %s", lastStr)
			}
		}
		return first, last, nil
	}
	return 0, 0, fmt.Errorf("invalid page range format: %s", s)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcmd := os.Args[1]
	switch subcmd {
	case "inspect":
		runInspect(os.Args[2:])
	case "audit":
		runAudit(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand %q. Expected 'inspect' or 'audit'\n", subcmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: verify <subcommand> [flags]")
	fmt.Println("Subcommands:")
	fmt.Println("  inspect   Inspect PDF extraction, lines, and parsed blocks for specific pages")
	fmt.Println("  audit     Perform strict quality audit on generated EPUB and compare with baseline")
}

// ---------------------------------------------------------
// INSPECT SUBCOMMAND
// ---------------------------------------------------------

func runInspect(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	pdfPath := fs.String("pdf", "", "Path to PDF file (required)")
	pagesStr := fs.String("pages", "1-5", "Page range (e.g. 15-16 or 15)")
	profileStr := fs.String("profile", "auto", "Profile (auto, fiction, technical)")
	stage := fs.String("stage", "all", "Pipeline stage (all, xml, lines, blocks, xhtml)")
	asJSON := fs.Bool("json", false, "Emit report as JSON")

	_ = fs.Parse(args)

	if *pdfPath == "" {
		fmt.Fprintf(os.Stderr, "Error: --pdf is required\n")
		fs.Usage()
		os.Exit(1)
	}

	firstPage, lastPage, err := parsePageRange(*pagesStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing --pages: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	rep, err := converter.InspectPDFPages(ctx, *pdfPath, firstPage, lastPage, converter.BookProfile(*profileStr))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Inspect failed: %v\n", err)
		os.Exit(1)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		return
	}

	// Human-readable formatted output
	fmt.Printf("=== PDF Inspection Report ===\n")
	fmt.Printf("File: %s\nTitle: %s | Author: %s | Pages in PDF: %d | Profile Used: %s\n", rep.PDFPath, rep.Title, rep.Author, rep.TotalPages, rep.ProfileUsed)
	fmt.Printf("Medians: Font Size=%.1fpt | Pitch=%.1fpx | Height=%.1fpx\n", rep.MedianFontSize, rep.MedianPitch, rep.MedianHeight)
	if len(rep.Bookmarks) > 0 {
		fmt.Printf("Bookmarks (%d):\n", len(rep.Bookmarks))
		for _, b := range rep.Bookmarks {
			fmt.Printf("  p.%-4d %s\n", b.Page, b.Title)
		}
	}
	fmt.Println()

	for _, p := range rep.Pages {
		fmt.Printf("--------------------------------------------------------------------------------\n")
		fmt.Printf("Page %d (%.1f x %.1f pt) — Texts: %d, Images: %d, RawLines: %d, CleanLines: %d, Blocks: %d\n",
			p.PageNum, p.Width, p.Height, len(p.RawTexts), len(p.RawImages), len(p.RawLines), len(p.CleanLines), len(p.Blocks))
		fmt.Printf("--------------------------------------------------------------------------------\n")

		if *stage == "all" || *stage == "xml" {
			if len(p.RawTexts) > 0 {
				fmt.Printf("[Raw Texts (%d)]:\n", len(p.RawTexts))
				for idx, t := range p.RawTexts {
					fmt.Printf("  [%3d] top=%-6.1f left=%-6.1f w=%-6.1f h=%-5.1f bot=%-6.1f sz=%-4.1f font=%-10s text=%q\n",
						idx, t.Top, t.Left, t.Width, t.Height, t.Bottom, t.FontSize, t.FontFamily, t.Text)
				}
			}
			if len(p.RawImages) > 0 {
				fmt.Printf("[Raw Images (%d)]:\n", len(p.RawImages))
				for idx, img := range p.RawImages {
					fmt.Printf("  [%3d] top=%-6.1f left=%-6.1f w=%-6.1f h=%-5.1f bot=%-6.1f src=%s\n",
						idx, img.Top, img.Left, img.Width, img.Height, img.Bottom, img.Src)
				}
			}
		}

		if *stage == "all" || *stage == "lines" {
			if len(p.NoiseRemoved) > 0 {
				fmt.Printf("[Stripped Noise (%d)]:\n", len(p.NoiseRemoved))
				for _, nl := range p.NoiseRemoved {
					fmt.Printf("  (NOISE) top=%-6.1f left=%-6.1f: %q\n", nl.Top, nl.Left, nl.PlainText)
				}
			}
			fmt.Printf("[Clean Visual Lines (%d)]:\n", len(p.CleanLines))
			for idx, cl := range p.CleanLines {
				fmt.Printf("  [%3d] top=%-6.1f left=%-6.1f sz=%-4.1f h=%-4.1f: %q\n", idx, cl.Top, cl.Left, cl.FontSize, cl.Height, cl.PlainText)
			}
		}

		if *stage == "all" || *stage == "blocks" {
			fmt.Printf("[Parsed Blocks (%d)]:\n", len(p.Blocks))
			for idx, b := range p.Blocks {
				kindStr := blockKindToString(b.Kind)
				fmt.Printf("  [%2d] %-14s (p.%d): %s\n", idx, kindStr, b.PageNum, summarizeBlock(b.XHTML))
			}
		}

		if *stage == "xhtml" {
			fmt.Printf("[XHTML Output]:\n")
			for _, b := range p.Blocks {
				fmt.Println(b.XHTML)
			}
		}
		fmt.Println()
	}
}

func blockKindToString(k converter.ParsedBlockKind) string {
	switch k {
	case converter.BlockHeading1:
		return "Heading1"
	case converter.BlockHeading2:
		return "Heading2"
	case converter.BlockHeading3:
		return "Heading3"
	case converter.BlockParagraph:
		return "Paragraph"
	case converter.BlockCode:
		return "Code"
	case converter.BlockTable:
		return "Table"
	case converter.BlockFigure:
		return "Figure"
	case converter.BlockSceneBreak:
		return "SceneBreak"
	case converter.BlockList:
		return "List"
	default:
		return fmt.Sprintf("Block(%d)", k)
	}
}

func summarizeBlock(xhtml string) string {
	s := strings.ReplaceAll(xhtml, "\n", " ")
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	runes := []rune(s)
	if len(runes) > 120 {
		return string(runes[:117]) + "..."
	}
	return s
}

// ---------------------------------------------------------
// AUDIT SUBCOMMAND
// ---------------------------------------------------------

type AuditScorecard struct {
	Name                       string   `json:"name"`
	Path                       string   `json:"path"`
	XMLErrors                  []string `json:"xml_errors"`
	BrokenLinks                []string `json:"broken_links"`
	BrokenImages               []string `json:"broken_images"`
	RawEscapedTags             []string `json:"raw_escaped_tags"`
	ScrambledDropCaps          []string `json:"scrambled_drop_caps"`
	CrossPageMidSentenceBreaks []string `json:"cross_page_mid_sentence_breaks"`
	TotalH1                    int      `json:"total_h1"`
	TotalChapters              int      `json:"total_chapters"`
	MultiH1Chapters            []string `json:"multi_h1_chapters"`
	TOCIssues                  []string `json:"toc_issues"`
	HasCover                   bool     `json:"has_cover"`
	TotalImages                int      `json:"total_images"`
	MicroSliceSpamCount        int      `json:"micro_slice_spam_count"`
}

type FullAuditReport struct {
	Rebind  AuditScorecard  `json:"rebind"`
	Calibre *AuditScorecard `json:"calibre,omitempty"`
	Passed  bool            `json:"passed"`
}

func runAudit(args []string) {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	pdfPath := fs.String("pdf", "sample/harry-potter-original-pdf.pdf", "Source PDF path")
	epubPath := fs.String("epub", "sample/harry-potter-rebind.epub", "rebind EPUB path")
	calibrePath := fs.String("calibre", "sample/harry-potter-calibre.epub", "Calibre EPUB path (optional)")
	asJSON := fs.Bool("json", false, "Emit audit report as JSON")
	strict := fs.Bool("strict", false, "Fail with exit code 1 if any critical defect found")

	_ = fs.Parse(args)

	if _, err := os.Stat(*epubPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: EPUB not found at %s\n", *epubPath)
		os.Exit(1)
	}

	rebindScorecard, err := auditEPUB(*epubPath, "rebind", true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Audit error for rebind: %v\n", err)
		os.Exit(1)
	}

	var calScorecard *AuditScorecard
	if *calibrePath != "" {
		if _, err := os.Stat(*calibrePath); err == nil {
			if sc, err := auditEPUB(*calibrePath, "Calibre", false); err == nil {
				calScorecard = sc
			}
		}
	}

	// Determine pass/fail
	passed := len(rebindScorecard.XMLErrors) == 0 &&
		len(rebindScorecard.BrokenLinks) == 0 &&
		len(rebindScorecard.BrokenImages) == 0 &&
		len(rebindScorecard.ScrambledDropCaps) == 0 &&
		len(rebindScorecard.CrossPageMidSentenceBreaks) == 0 &&
		len(rebindScorecard.TOCIssues) == 0 &&
		rebindScorecard.TotalImages > 0

	rep := FullAuditReport{
		Rebind:  *rebindScorecard,
		Calibre: calScorecard,
		Passed:  passed,
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		if *strict && !passed {
			os.Exit(1)
		}
		return
	}

	// Markdown comparison table
	fmt.Printf("\n# EPUB Quality Audit Report\n\n")
	fmt.Printf("• Source PDF:   `%s`\n", *pdfPath)
	fmt.Printf("• rebind:       `%s`\n", *epubPath)
	if calScorecard != nil {
		fmt.Printf("• Calibre Base: `%s`\n", *calibrePath)
	}
	fmt.Println()

	fmt.Printf("| Metric | rebind | Calibre Baseline | Quality Verdict |\n")
	fmt.Printf("| :--- | :--- | :--- | :--- |\n")

	// 1. XML Errors
	calXMLErrStr := "N/A"
	if calScorecard != nil {
		calXMLErrStr = strconv.Itoa(len(calScorecard.XMLErrors))
	}
	rebindXMLErrStr := strconv.Itoa(len(rebindScorecard.XMLErrors))
	v1 := "✅ Clean"
	if len(rebindScorecard.XMLErrors) > 0 {
		v1 = "❌ Fail"
	}
	fmt.Printf("| XML Validity Errors | %s | %s | %s |\n", rebindXMLErrStr, calXMLErrStr, v1)

	// 2. Broken Internal Links / Images
	calBrokenStr := "N/A"
	if calScorecard != nil {
		calBrokenStr = strconv.Itoa(len(calScorecard.BrokenLinks) + len(calScorecard.BrokenImages))
	}
	rebindBrokenTotal := len(rebindScorecard.BrokenLinks) + len(rebindScorecard.BrokenImages)
	v2 := "✅ Clean"
	if rebindBrokenTotal > 0 {
		v2 = "❌ Broken Links/Assets"
	}
	fmt.Printf("| Broken Links / Missing Images | %d | %s | %s |\n", rebindBrokenTotal, calBrokenStr, v2)

	// 3. Scrambled Drop Caps
	calDropStr := "N/A"
	if calScorecard != nil {
		calDropStr = fmt.Sprintf("%d scrambled", len(calScorecard.ScrambledDropCaps))
	}
	v3 := "✅ Clean"
	if len(rebindScorecard.ScrambledDropCaps) > 0 {
		v3 = fmt.Sprintf("❌ %d Scrambled", len(rebindScorecard.ScrambledDropCaps))
	}
	fmt.Printf("| Chapter Opening Sentences (Drop Caps) | %d scrambled | %s | %s |\n",
		len(rebindScorecard.ScrambledDropCaps), calDropStr, v3)

	// 4. Mid-Sentence Cross-Page Breaks
	calCrossStr := "N/A"
	if calScorecard != nil {
		calCrossStr = fmt.Sprintf("%d mid-sentence breaks", len(calScorecard.CrossPageMidSentenceBreaks))
	}
	v4 := "✅ Seamless"
	if len(rebindScorecard.CrossPageMidSentenceBreaks) > 0 {
		v4 = fmt.Sprintf("⚠️ %d breaks", len(rebindScorecard.CrossPageMidSentenceBreaks))
	}
	fmt.Printf("| Mid-Sentence Cross-Page Splits | %d | %s | %s |\n",
		len(rebindScorecard.CrossPageMidSentenceBreaks), calCrossStr, v4)

	// 5. Total Chapters & H1 Ratio
	calH1Str := "N/A"
	if calScorecard != nil {
		calH1Str = fmt.Sprintf("%d files / %d H1s", calScorecard.TotalChapters, calScorecard.TotalH1)
	}
	v5 := "✅ Clean Hierarchy"
	if len(rebindScorecard.MultiH1Chapters) > 0 {
		v5 = fmt.Sprintf("⚠️ %d multi-H1 files", len(rebindScorecard.MultiH1Chapters))
	}
	fmt.Printf("| Total Chapters / Semantic &lt;h1&gt; Ratio | %d files / %d H1s | %s | %s |\n",
		rebindScorecard.TotalChapters, rebindScorecard.TotalH1, calH1Str, v5)

	// 6. Clean TOC
	calTOCStr := "N/A"
	if calScorecard != nil {
		calTOCStr = fmt.Sprintf("%d issues", len(calScorecard.TOCIssues))
	}
	v6 := "✅ Clean"
	if len(rebindScorecard.TOCIssues) > 0 {
		v6 = fmt.Sprintf("❌ %d TOC issues", len(rebindScorecard.TOCIssues))
	}
	fmt.Printf("| Clean TOC Titles (no page noise / no shift) | %d issues | %s | %s |\n",
		len(rebindScorecard.TOCIssues), calTOCStr, v6)

	// 7. Embedded Images & Cover
	calImgStr := "N/A"
	if calScorecard != nil {
		calImgStr = fmt.Sprintf("%d images (%d micro-slices)", calScorecard.TotalImages, calScorecard.MicroSliceSpamCount)
	}
	v7 := "✅ Clean Cover & Art"
	if rebindScorecard.TotalImages == 0 || !rebindScorecard.HasCover {
		v7 = "❌ Missing Images or Cover"
	}
	coverLabel := "No Cover"
	if rebindScorecard.HasCover {
		coverLabel = "Cover OK"
	}
	fmt.Printf("| Embedded Images & Micro-Slice Spam | %d images (%s, %d slices) | %s | %s |\n\n",
		rebindScorecard.TotalImages, coverLabel, rebindScorecard.MicroSliceSpamCount, calImgStr, v7)

	// Detailed Diagnostic Findings
	if len(rebindScorecard.ScrambledDropCaps) > 0 {
		fmt.Printf("### Scrambled Chapter Drop Caps in rebind (%d):\n", len(rebindScorecard.ScrambledDropCaps))
		for _, s := range rebindScorecard.ScrambledDropCaps {
			fmt.Printf("  • %s\n", s)
		}
		fmt.Println()
	}

	if len(rebindScorecard.TOCIssues) > 0 {
		fmt.Printf("### TOC Issues in rebind (%d):\n", len(rebindScorecard.TOCIssues))
		for _, s := range rebindScorecard.TOCIssues {
			fmt.Printf("  • %s\n", s)
		}
		fmt.Println()
	}

	if len(rebindScorecard.CrossPageMidSentenceBreaks) > 0 {
		fmt.Printf("### Mid-Sentence Cross-Page Splits in rebind (%d total, showing first 5):\n", len(rebindScorecard.CrossPageMidSentenceBreaks))
		for i, s := range rebindScorecard.CrossPageMidSentenceBreaks {
			if i >= 5 {
				fmt.Printf("  ... and %d more\n", len(rebindScorecard.CrossPageMidSentenceBreaks)-5)
				break
			}
			fmt.Printf("  • %s\n", s)
		}
		fmt.Println()
	}

	if *strict && !passed {
		fmt.Fprintf(os.Stderr, "Audit Failed in strict mode: Critical quality defects found in %s\n", *epubPath)
		os.Exit(1)
	}
}

// auditEPUB opens an EPUB zip and evaluates all 6 quality checks.
func auditEPUB(epubPath, label string, isStrictXML bool) (*AuditScorecard, error) {
	zr, err := zip.OpenReader(epubPath)
	if err != nil {
		return nil, fmt.Errorf("open epub zip %s: %w", epubPath, err)
	}
	defer zr.Close()

	card := &AuditScorecard{
		Name: label,
		Path: epubPath,
	}

	// 1. Index all files in ZIP
	zipFiles := make(map[string]*zip.File)
	for _, f := range zr.File {
		zipFiles[f.Name] = f
		// Count image files
		ext := strings.ToLower(filepath.Ext(f.Name))
		if ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".webp" || ext == ".gif" || ext == ".svg" {
			card.TotalImages++
		}
	}

	// 2. Find OPF package file from META-INF/container.xml
	opfPath := findOPFPath(zr)
	if opfPath == "" {
		card.XMLErrors = append(card.XMLErrors, "Missing META-INF/container.xml or rootfile")
		return card, nil
	}

	opfFile, ok := zipFiles[opfPath]
	if !ok {
		card.XMLErrors = append(card.XMLErrors, fmt.Sprintf("Rootfile %s not found in zip", opfPath))
		return card, nil
	}

	opfData, err := readZipFile(opfFile)
	if err != nil {
		card.XMLErrors = append(card.XMLErrors, fmt.Sprintf("Read %s error: %v", opfPath, err))
		return card, nil
	}

	opfBaseDir := path.Dir(opfPath)
	if opfBaseDir == "." {
		opfBaseDir = ""
	}

	// Check OPF XML validity
	if err := checkXMLValidity(opfData, isStrictXML); err != nil {
		card.XMLErrors = append(card.XMLErrors, fmt.Sprintf("OPF %s XML error: %v", opfPath, err))
	}

	// Parse manifest and cover from OPF
	manifestHrefs, coverHref, tocHref := parseOPFManifest(opfData)
	if coverHref != "" {
		card.HasCover = true
	}

	// Check TOC file (NCX or Nav)
	if tocHref != "" {
		fullTocPath := resolveZipPath(opfBaseDir, tocHref)
		if f, ok := zipFiles[fullTocPath]; ok {
			tocData, _ := readZipFile(f)
			card.TOCIssues = append(card.TOCIssues, auditTOC(tocData)...)
		}
	}

	// 3. Scan HTML / XHTML document files
	var htmlPaths []string
	for href, mediaType := range manifestHrefs {
		if strings.Contains(mediaType, "html") || strings.Contains(mediaType, "xhtml") {
			fullPath := resolveZipPath(opfBaseDir, href)
			htmlPaths = append(htmlPaths, fullPath)
		}
	}
	card.TotalChapters = len(htmlPaths)

	// Collect all anchors/IDs in all HTML files for link resolution
	fileIDs := make(map[string]map[string]bool)
	for _, p := range htmlPaths {
		if f, ok := zipFiles[p]; ok {
			data, _ := readZipFile(f)
			fileIDs[p] = extractIDs(data)
		}
	}

	// Regexes for audit
	imgSrcRegex := regexp.MustCompile(`(?i)<img[^>]+src=["']([^"']+)["']`)
	aHrefRegex := regexp.MustCompile(`(?i)<a[^>]+href=["']([^"']+)["']`)
	h1Regex := regexp.MustCompile(`(?i)<h1\b[^>]*>(.*?)</h1>`)
	pRegex := regexp.MustCompile(`(?is)<p\b[^>]*>(.*?)</p>`)
	pageAnchorRegex := regexp.MustCompile(`(?i)<(?:div|span)\s+id=["']p(\d+)["'][^>]*>\s*</(?:div|span)>`)

	for _, htmlPath := range htmlPaths {
		f, ok := zipFiles[htmlPath]
		if !ok {
			continue
		}
		data, err := readZipFile(f)
		if err != nil {
			card.XMLErrors = append(card.XMLErrors, fmt.Sprintf("Read %s error: %v", htmlPath, err))
			continue
		}

		htmlStr := string(data)
		htmlBaseDir := path.Dir(htmlPath)
		if htmlBaseDir == "." {
			htmlBaseDir = ""
		}

		// XML check
		if err := checkXMLValidity(data, isStrictXML); err != nil {
			card.XMLErrors = append(card.XMLErrors, fmt.Sprintf("%s XML syntax error: %v", htmlPath, err))
		}

		// Escaped raw HTML check
		for _, tag := range []string{"&lt;a ", "&lt;i&gt;", "&lt;em&gt;", "&lt;strong&gt;", "&lt;p&gt;"} {
			if strings.Contains(htmlStr, tag) {
				card.RawEscapedTags = append(card.RawEscapedTags, fmt.Sprintf("%s contains %s", htmlPath, tag))
			}
		}

		// Broken images
		imgMatches := imgSrcRegex.FindAllStringSubmatch(htmlStr, -1)
		consecutiveMicroImages := 0
		for _, m := range imgMatches {
			src := m[1]
			resolved := resolveZipPath(htmlBaseDir, src)
			imgZip, exists := zipFiles[resolved]
			if !exists || imgZip.UncompressedSize64 == 0 {
				card.BrokenImages = append(card.BrokenImages, fmt.Sprintf("%s: broken image %s -> %s", htmlPath, src, resolved))
			}
			// Micro-slice heuristic: strip sequence of many small slices
			if strings.Contains(src, "book-7_") || strings.Contains(src, "strip") {
				consecutiveMicroImages++
			}
		}
		if consecutiveMicroImages >= 10 {
			card.MicroSliceSpamCount += consecutiveMicroImages
		}

		// Broken internal links
		aMatches := aHrefRegex.FindAllStringSubmatch(htmlStr, -1)
		for _, m := range aMatches {
			href := m[1]
			if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "mailto:") {
				continue
			}
			parts := strings.Split(href, "#")
			targetFile := htmlPath
			if parts[0] != "" {
				targetFile = resolveZipPath(htmlBaseDir, parts[0])
			}
			if _, exists := zipFiles[targetFile]; !exists {
				card.BrokenLinks = append(card.BrokenLinks, fmt.Sprintf("%s: broken link %s -> file %s not in zip", htmlPath, href, targetFile))
				continue
			}
			if len(parts) > 1 && parts[1] != "" {
				frag := parts[1]
				if ids, ok := fileIDs[targetFile]; ok {
					if !ids[frag] {
						card.BrokenLinks = append(card.BrokenLinks, fmt.Sprintf("%s: broken anchor %s in file %s", htmlPath, href, targetFile))
					}
				}
			}
		}

		// Count <h1> tags
		h1s := h1Regex.FindAllStringSubmatch(htmlStr, -1)
		card.TotalH1 += len(h1s)
		if len(h1s) > 1 {
			card.MultiH1Chapters = append(card.MultiH1Chapters, fmt.Sprintf("%s has %d <h1> tags", htmlPath, len(h1s)))
		}

		// Drop cap check & Scrambled chapter openings
		pMatches := pRegex.FindAllStringSubmatch(htmlStr, -1)
		var pTexts []string
		for _, pm := range pMatches {
			clean := cleanHTMLForAnalysis(pm[1])
			if clean != "" {
				pTexts = append(pTexts, clean)
			}
		}

		for idx, pt := range pTexts {
			// Check scrambled pattern:
			// Paragraph 0 starts with Single Upper letter followed by space and lowercase, or standalone Upper
			// and paragraph 1 starts with lowercase or continuation word.
			if idx == 0 && len(pTexts) > 1 {
				p0 := pt
				p1 := pTexts[1]

				// Signature A: Next paragraph starts with lowercase letter or apostrophe
				firstRuneP1 := firstLetterRune(p1)
				if unicode.IsLower(firstRuneP1) || strings.HasPrefix(p1, "’") || strings.HasPrefix(p1, "'") {
					card.ScrambledDropCaps = append(card.ScrambledDropCaps,
						fmt.Sprintf("%s: Scrambled drop cap! P1 starts with %q, but P2 begins with lowercase %q",
							htmlPath, truncate(p0, 40), truncate(p1, 40)))
					continue
				}

				// Signature B: Starts with single letter non-word like "H “Out of here”"
				if isSingleLetterDropCapGlitch(p0) {
					card.ScrambledDropCaps = append(card.ScrambledDropCaps,
						fmt.Sprintf("%s: Glitched drop cap lead: %q", htmlPath, truncate(p0, 50)))
				}
			}
		}

		// Mid-sentence cross-page breaks
		// Look for <p>...</p>\s*<div id="pN"></div>\s*<p>...
		splitMatches := pageAnchorRegex.FindAllStringIndex(htmlStr, -1)
		for _, idxPair := range splitMatches {
			anchorStart := idxPair[0]
			anchorEnd := idxPair[1]

			preceding := htmlStr[:anchorStart]
			succeeding := htmlStr[anchorEnd:]

			lastCloseP := strings.LastIndex(preceding, "</p>")
			nextOpenP := strings.Index(succeeding, "<p")

			if lastCloseP != -1 && nextOpenP != -1 {
				betweenBefore := strings.TrimSpace(preceding[lastCloseP+4:])
				betweenAfter := strings.TrimSpace(succeeding[:nextOpenP])
				if betweenBefore != "" || betweenAfter != "" {
					continue
				}

				// Text right before </p>
				pBefore := preceding[:lastCloseP]
				openPIdx := strings.LastIndex(pBefore, "<p")
				if openPIdx != -1 {
					plainBefore := cleanHTMLForAnalysis(pBefore[openPIdx:])
					// Text right after <p>
					closePIdx := strings.Index(succeeding[nextOpenP:], "</p>")
					if closePIdx != -1 {
						plainAfter := cleanHTMLForAnalysis(succeeding[nextOpenP : nextOpenP+closePIdx])
						if len(plainBefore) > 0 && len(plainAfter) > 0 {
							endsTerminal := endsWithTerminalPunct(plainBefore)
							startsLower := unicode.IsLower(firstLetterRune(plainAfter))
							startsDialogue := strings.HasPrefix(strings.TrimSpace(plainAfter), "“") ||
								strings.HasPrefix(strings.TrimSpace(plainAfter), "\"") ||
								strings.HasPrefix(strings.TrimSpace(plainAfter), "‘") ||
								strings.HasPrefix(strings.TrimSpace(plainAfter), "'")
							if !endsTerminal && (startsLower && !startsDialogue) {
								card.CrossPageMidSentenceBreaks = append(card.CrossPageMidSentenceBreaks,
									fmt.Sprintf("%s: %q ... / ... %q", htmlPath, tail(plainBefore, 30), head(plainAfter, 30)))
							}
						}
					}
				}
			}
		}
	}

	return card, nil
}

func checkXMLValidity(data []byte, strict bool) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = strict
	for {
		_, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func findOPFPath(zr *zip.ReadCloser) string {
	for _, f := range zr.File {
		if f.Name == "META-INF/container.xml" {
			data, err := readZipFile(f)
			if err != nil {
				return ""
			}
			r := regexp.MustCompile(`full-path=["']([^"']+)["']`)
			m := r.FindSubmatch(data)
			if len(m) > 1 {
				return string(m[1])
			}
		}
	}
	return ""
}

func parseOPFManifest(opfData []byte) (map[string]string, string, string) {
	manifest := make(map[string]string)
	coverHref := ""
	tocHref := ""

	itemRegex := regexp.MustCompile(`(?i)<item\b([^>]+?)(?:/>|>)`)
	attrRegex := regexp.MustCompile(`(?i)(href|media-type|id|properties)=["']([^"']+)["']`)

	items := itemRegex.FindAllSubmatch(opfData, -1)
	for _, it := range items {
		attrs := attrRegex.FindAllSubmatch(it[1], -1)
		var href, mediaType, id, props string
		for _, a := range attrs {
			k := strings.ToLower(string(a[1]))
			v := string(a[2])
			switch k {
			case "href":
				href = v
			case "media-type":
				mediaType = v
			case "id":
				id = v
			case "properties":
				props = v
			}
		}
		if href != "" {
			manifest[href] = mediaType
			if strings.Contains(props, "cover-image") || id == "cover" || strings.Contains(id, "cover-image") {
				coverHref = href
			}
			if id == "ncx" || strings.Contains(props, "nav") || strings.HasSuffix(href, ".ncx") {
				if tocHref == "" {
					tocHref = href
				}
			}
		}
	}
	return manifest, coverHref, tocHref
}

func auditTOC(tocData []byte) []string {
	var issues []string
	tocStr := string(tocData)

	// Check for trailing page numbers like "· 1" or "• 42"
	pageSuffixRegex := regexp.MustCompile(`[·•]\s*\d+\s*<`)
	if pageSuffixRegex.MatchString(tocStr) {
		issues = append(issues, "TOC retains print page number suffixes (e.g. '· 1')")
	}

	// Check NCX-specific standards compliance
	if strings.Contains(tocStr, "<ncx") {
		uidRegex := regexp.MustCompile(`(?i)<meta\b[^>]*name=["']dtb:uid["'][^>]*content=["']([^"']+)["']|<meta\b[^>]*content=["']([^"']+)["'][^>]*name=["']dtb:uid["']`)
		if !uidRegex.MatchString(tocStr) {
			issues = append(issues, "toc.ncx missing mandatory dtb:uid meta tag")
		}

		depthRegex := regexp.MustCompile(`(?i)<meta\b[^>]*name=["']dtb:depth["'][^>]*content=["']([^"']+)["']|<meta\b[^>]*content=["']([^"']+)["'][^>]*name=["']dtb:depth["']`)
		if m := depthRegex.FindStringSubmatch(tocStr); len(m) > 1 {
			depthVal := m[1]
			if depthVal == "" && len(m) > 2 {
				depthVal = m[2]
			}
			if _, err := strconv.Atoi(depthVal); err != nil {
				issues = append(issues, fmt.Sprintf("toc.ncx has non-integer dtb:depth: %q", depthVal))
			}
		}

		navPoints := regexp.MustCompile(`(?i)<navPoint\b[^>]*>`).FindAllString(tocStr, -1)
		missingPlayOrder := 0
		for _, np := range navPoints {
			if !strings.Contains(strings.ToLower(np), "playorder=") {
				missingPlayOrder++
			}
		}
		if missingPlayOrder > 0 {
			issues = append(issues, fmt.Sprintf("toc.ncx has %d navPoint entries missing required playOrder attribute", missingPlayOrder))
		}
	}

	// Check for Roman numerals at end of book (the chapter_034 "vii" bug)
	// Check if Roman numeral bookmarks appear after high chapter numbers
	lines := strings.Split(tocStr, "\n")
	seenHighChapter := false
	for _, l := range lines {
		if strings.Contains(l, "Chapter 20") || strings.Contains(l, "Chapter 30") || strings.Contains(l, "Chapter Thirty") || strings.Contains(l, "CHAPTER THIRTY") || strings.Contains(l, "Credits") {
			seenHighChapter = true
		}
		if seenHighChapter {
			rRegex := regexp.MustCompile(`(?i)>\s*(vii|viii|ix|x)\s*<`)
			if rRegex.MatchString(l) {
				issues = append(issues, "Ghost Roman numeral TOC item ('vii'..'x') shifted to end of book")
				break
			}
		}
	}

	return issues
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func resolveZipPath(baseDir, relative string) string {
	relative = strings.TrimPrefix(relative, "/")
	if baseDir == "" {
		return path.Clean(relative)
	}
	return path.Clean(path.Join(baseDir, relative))
}

func extractIDs(data []byte) map[string]bool {
	ids := make(map[string]bool)
	idRegex := regexp.MustCompile(`(?i)\bid=["']([^"']+)["']`)
	matches := idRegex.FindAllSubmatch(data, -1)
	for _, m := range matches {
		ids[string(m[1])] = true
	}
	return ids
}

var (
	htmlTagRegex       = regexp.MustCompile(`<[^>]+>`)
	dropCapSpanRegex   = regexp.MustCompile(`<span class="dropcap">([^<]+)</span>`)
)

func cleanHTMLForAnalysis(html string) string {
	s := dropCapSpanRegex.ReplaceAllString(html, "$1")
	s = htmlTagRegex.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&rsquo;", "’")
	s = strings.ReplaceAll(s, "&lsquo;", "‘")
	s = strings.ReplaceAll(s, "&rdquo;", "”")
	s = strings.ReplaceAll(s, "&ldquo;", "“")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(s, " "))
}

func firstLetterRune(s string) rune {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return r
		}
	}
	return 0
}

func endsWithTerminalPunct(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return false
	}
	runes := []rune(s)
	r := runes[len(runes)-1]
	if r == '.' || r == '!' || r == '?' || r == '"' || r == '”' || r == '’' || r == '\'' || r == ':' || r == '…' {
		return true
	}
	// Check for trailing quote after terminal punctuation
	if len(runes) >= 2 {
		rPrev := runes[len(runes)-2]
		if (r == '"' || r == '”' || r == '\'' || r == '’') && (rPrev == '.' || rPrev == '!' || rPrev == '?' || rPrev == ',') {
			return true
		}
	}
	return false
}

func isSingleLetterDropCapGlitch(s string) bool {
	runes := []rune(s)
	if len(runes) < 2 {
		return false
	}
	// Check for things like: H “Out of here...
	r := runes[0]
	if unicode.IsUpper(r) && r != 'A' && r != 'I' {
		if len(runes) > 2 && (runes[1] == ' ' || runes[1] == '"' || runes[1] == '“' || runes[1] == '”') {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
