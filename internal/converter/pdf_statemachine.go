package converter

import (
	"bytes"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// RegionCropper renders a vector diagram or complex table into a PNG and returns the EPUB image path.
type RegionCropper func(pageNum int, left, top, width, height float64, label string) (string, error)

// EmbeddedImageAdder copies an extracted raster image into the EPUB and returns the EPUB image path.
type EmbeddedImageAdder func(src string) (string, error)

type ParsedBlockKind int

const (
	BlockHeading1 ParsedBlockKind = iota
	BlockHeading2
	BlockHeading3
	BlockParagraph
	BlockCode
	BlockTable
	BlockFigure
	BlockSceneBreak
	BlockList
)

type ParsedBlock struct {
	Kind    ParsedBlockKind
	XHTML   string
	Title   string // Title if Heading
	PageNum int
	Top     float64
}

var (
	pageNumNoiseRegex  = regexp.MustCompile(`^[\s\p{P}\p{S}\x{0091}\x{0092}\d]+$`)
	tocChapterNumRegex = regexp.MustCompile(`(?i)^(?:one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|twenty-one|twenty-two|twenty-three|twenty-four|twenty-five|twenty-six|twenty-seven|twenty-eight|twenty-nine|thirty)$`)
	techCaptionRegex   = regexp.MustCompile(`(?i)^(?:Figure|Fig\.|Table|Listing|Algorithm)\s+\d+`)
	tableCaptionRegex  = regexp.MustCompile(`(?i)^Table\s+\d+`)
	figureCaptionRegex = regexp.MustCompile(`(?i)^(?:Figure|Fig\.|Diagram|Chart|Plate)\s+\d+`)
	heading1Regex      = regexp.MustCompile(`(?i)^(?:chapter|part|book|prologue|epilogue)\s+([0-9]+|[ivxlcdm]+|one|two|three|four|five|six|seven|eight|nine|ten)\b`)
	listItemRegex      = regexp.MustCompile(`^(?:[•◦▪–*\-]|\d+[\.\)]|\([a-zA-Z0-9]+\))\s+(.*)$`)
	dialogueLeadRegex  = regexp.MustCompile(`^["“‘—–«']`)
)

func isSceneBreakLine(s string) bool {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) == 0 || len(trimmed) > 20 {
		return false
	}
	symbolCount := 0
	for _, r := range trimmed {
		if r == '*' || r == '•' || r == '~' || r == '-' || r == '—' || r == '§' || r == '#' || r == '⋄' {
			symbolCount++
		} else if !unicode.IsSpace(r) {
			return false
		}
	}
	return symbolCount >= 1 && symbolCount <= 8
}

var boxDrawingRunes = map[rune]bool{
	'┌': true, '┐': true, '└': true, '┘': true,
	'─': true, '│': true, '├': true, '┤': true,
	'┬': true, '┴': true, '┼': true, '▼': true,
	'▲': true, '►': true, '◄': true, '╭': true,
	'╮': true, '╯': true, '╰': true,
}

var codeSyntaxTokens = []string{
	"func ", "package ", "import ", "def ", "class ", "return ",
	"const ", "let ", "var ", "if (", "for (", "{", "}", "();",
	"=>", "->", "//",
	"public ", "private ", "protected ", "interface ", "abstract ", "void ", "type ",
}

// ResolveBookProfile auto-detects whether the document is Technical or Fiction.
func ResolveBookProfile(doc *xmlDocument, pages [][]VisualLine) BookProfile {
	totalRuns := 0
	monoRuns := 0
	techCaptions := 0
	codeLines := 0
	totalLines := 0

	if doc != nil {
		for _, p := range doc.Pages {
			totalRuns += len(p.Texts)
		}
	}

	for _, lines := range pages {
		totalLines += len(lines)
		for _, l := range lines {
			if l.IsMonospace {
				monoRuns++
			}
			if techCaptionRegex.MatchString(l.PlainText) {
				techCaptions++
			}
			if isCodeLine(l) {
				codeLines++
			}
		}
	}

	monoRatio := 0.0
	if totalRuns > 0 {
		monoRatio = float64(monoRuns) / float64(totalRuns)
	}

	codeRatio := 0.0
	if totalLines > 0 {
		codeRatio = float64(codeLines) / float64(totalLines)
	}

	if monoRatio >= 0.025 || (techCaptions >= 2 && len(pages) <= 30) || (techCaptions >= 5 && float64(techCaptions)/float64(len(pages)) >= 0.05) || (codeLines >= 6 && len(pages) <= 10) || codeRatio >= 0.04 {
		return ProfileTechnical
	}
	return ProfileFiction
}

func countBoxDrawing(s string) int {
	count := 0
	for _, r := range s {
		if boxDrawingRunes[r] {
			count++
		}
	}
	return count
}

func isCodeLine(l VisualLine) bool {
	if l.IsMonospace {
		return true
	}
	if countBoxDrawing(l.PlainText) >= 2 {
		return true
	}
	trimmed := strings.TrimSpace(l.PlainText)
	for _, token := range codeSyntaxTokens {
		if strings.HasPrefix(trimmed, token) || strings.HasSuffix(trimmed, token) {
			return true
		}
	}
	return false
}

// DocumentLexicon holds word frequencies for accurate de-hyphenation.
type DocumentLexicon struct {
	unsplitWords    map[string]int
	hyphenatedWords map[string]int
}

var compoundPrefixes = map[string]bool{
	"self": true, "well": true, "all": true, "ex": true,
	"non": true, "cross": true, "half": true, "high": true,
	"low": true, "state": true, "quasi": true, "ultra": true,
	"anti": true, "co": true, "meta": true,
}

var wordCleanRegex = regexp.MustCompile(`^[^\p{L}\p{N}]+|[^\p{L}\p{N}]+$`)

// BuildDocumentLexicon scans the document to build frequency tables of intact words vs hyphenated words.
func BuildDocumentLexicon(pages [][]VisualLine) *DocumentLexicon {
	lex := &DocumentLexicon{
		unsplitWords:    make(map[string]int),
		hyphenatedWords: make(map[string]int),
	}

	for _, lines := range pages {
		for _, l := range lines {
			words := strings.Fields(l.PlainText)
			if len(words) <= 1 {
				continue
			}
			// Middle words that are definitely not at line wraps
			for _, w := range words[:len(words)-1] {
				w = wordCleanRegex.ReplaceAllString(w, "")
				if strings.Contains(w, "-") {
					lex.hyphenatedWords[strings.ToLower(w)]++
				} else if len(w) > 2 {
					lex.unsplitWords[strings.ToLower(w)]++
				}
			}
		}
	}

	return lex
}

// Dehyphenate decides whether to join word1 and word2 or keep the hyphen.
func (lex *DocumentLexicon) Dehyphenate(word1, word2 string) string {
	word1 = strings.TrimSuffix(word1, "-")
	if word1 == "" {
		return word2
	}
	if word2 == "" {
		return word1
	}

	// 1. If word2 starts with uppercase (e.g. non-European), keep hyphen
	if len(word2) > 0 && unicode.IsUpper(rune(word2[0])) {
		return word1 + "-" + word2
	}

	joined := strings.ToLower(word1 + word2)
	hyph := strings.ToLower(word1 + "-" + word2)

	// 2. Frequency lookup in document lexicon
	if lex.hyphenatedWords[hyph] > lex.unsplitWords[joined] {
		return word1 + "-" + word2
	}
	if lex.unsplitWords[joined] > 0 {
		return word1 + word2
	}

	// 3. Known compound prefix
	if compoundPrefixes[strings.ToLower(word1)] {
		return word1 + "-" + word2
	}

	// Default: strip hyphen
	return word1 + word2
}

// StateMachineConfig provides parameters and callbacks for PDF reflow parsing.
type StateMachineConfig struct {
	Profile       BookProfile
	CropRegion    RegionCropper
	AddImage      EmbeddedImageAdder
	Lexicon       *DocumentLexicon
	MedianPitch   float64
	MedianHeight  float64
	PageWidth     float64
	PageHeight    float64
	TypicalLeft   float64
	TypicalRight  float64
}

// ParsePDFPages runs the deterministic 4-state parser across the document.
func ParsePDFPages(doc *xmlDocument, pages [][]VisualLine, config StateMachineConfig) []ParsedBlock {
	// 1. Calculate document-wide typography baselines
	var allFontSizes []float64
	var allPitches []float64
	var allHeights []float64

	for pIdx, lines := range pages {
		for i, l := range lines {
			if l.FontSize > 0 {
				allFontSizes = append(allFontSizes, l.FontSize)
			}
			if l.Height > 0 {
				allHeights = append(allHeights, l.Height)
			}
			if i+1 < len(lines) {
				next := lines[i+1]
				pitch := next.Top - l.Top
				if pitch >= 8.0 && pitch <= 45.0 {
					allPitches = append(allPitches, pitch)
				}
			}
		}
		_ = pIdx
	}

	medianFontSize := median(allFontSizes, 12.0)
	medianPitch := median(allPitches, 14.0)
	medianHeight := median(allHeights, 12.0)

	config.MedianPitch = medianPitch
	config.MedianHeight = medianHeight

	if config.Lexicon == nil {
		config.Lexicon = BuildDocumentLexicon(pages)
	}

	// 2. Process page by page
	var blocks []ParsedBlock

	for pIdx, lines := range pages {
		pageNum := pIdx + 1
		if doc != nil && pIdx < len(doc.Pages) && doc.Pages[pIdx].Number > 0 {
			pageNum = doc.Pages[pIdx].Number
		}
		var pageImages []xmlImage
		if doc != nil && pIdx < len(doc.Pages) {
			pageImages = doc.Pages[pIdx].Images
		}

		pageBlocks := parseSinglePage(lines, pageImages, pageNum, medianFontSize, config)
		blocks = append(blocks, pageBlocks...)
	}

	return blocks
}

func stripHeadingTags(s string) string {
	s = strings.TrimPrefix(s, "<h1>")
	s = strings.TrimSuffix(s, "</h1>")
	s = strings.TrimPrefix(s, "<h2>")
	s = strings.TrimSuffix(s, "</h2>")
	s = strings.TrimPrefix(s, "<h3>")
	s = strings.TrimSuffix(s, "</h3>")
	return strings.TrimSpace(s)
}

func parseSinglePage(lines []VisualLine, images []xmlImage, pageNum int, medianFontSize float64, config StateMachineConfig) []ParsedBlock {
	var blocks []ParsedBlock
	consumedLines := make(map[int]bool)
	consumedImages := make(map[int]bool)

	// Step A1: Group vertically stacked micro-strips (e.g. Page 7's 231 1px slices)
	for i := 0; i < len(images); i++ {
		if consumedImages[i] {
			continue
		}
		curr := images[i]
		group := []int{i}
		minLeft := curr.Left
		minTop := curr.Top
		maxRight := curr.Left + curr.Width
		maxBottom := curr.Top + curr.Height

		for j := i + 1; j < len(images); j++ {
			if consumedImages[j] {
				continue
			}
			next := images[j]
			overlap := math.Min(maxRight, next.Left+next.Width) - math.Max(minLeft, next.Left)
			isMicro := curr.Height <= 8.0 || next.Height <= 8.0 || (len(group) > 1 && next.Height <= 25.0)
			if isMicro && math.Abs(next.Top-maxBottom) <= 4.0 && overlap > 20.0 {
				group = append(group, j)
				if next.Left < minLeft {
					minLeft = next.Left
				}
				if next.Top < minTop {
					minTop = next.Top
				}
				if next.Left+next.Width > maxRight {
					maxRight = next.Left + next.Width
				}
				if next.Top+next.Height > maxBottom {
					maxBottom = next.Top + next.Height
				}
			} else if next.Top > maxBottom+20.0 {
				break
			}
		}

		if len(group) >= 4 && config.CropRegion != nil {
			for _, idx := range group {
				consumedImages[idx] = true
			}
			cropW := maxRight - minLeft
			cropH := maxBottom - minTop
			cropPath, err := config.CropRegion(pageNum, minLeft, minTop, cropW, cropH, fmt.Sprintf("strip_p%03d_%02d", pageNum, i))
			if err == nil {
				blocks = append(blocks, ParsedBlock{
					Kind:    BlockFigure,
					XHTML:   fmt.Sprintf("<figure><img src=\"%s\" alt=\"Illustration\"/></figure>", cropPath),
					PageNum: pageNum,
					Top:     minTop,
				})
			}
		}
	}

	// Step A2: Process remaining individual images
	for imgIdx, img := range images {
		if consumedImages[imgIdx] || config.AddImage == nil {
			continue
		}
		// Skip full-bleed cover/flap images on pages 1-3 if there's no body text
		isCoverPage := pageNum <= 3 && img.Width >= 0.65*img.Width && img.Height >= 500 && len(lines) <= 1
		if isCoverPage {
			consumedImages[imgIdx] = true
			continue
		}

		epubPath, err := config.AddImage(img.Src)
		if err != nil {
			continue
		}
		consumedImages[imgIdx] = true

		// Find caption within 45px above or below
		captionRich := ""
		for lineIdx, l := range lines {
			if consumedLines[lineIdx] {
				continue
			}
			distBelow := l.Top - (img.Top + img.Height)
			distAbove := img.Top - (l.Top + l.Height)

			if (distBelow >= -5.0 && distBelow <= 45.0) || (distAbove >= -5.0 && distAbove <= 45.0) {
				if figureCaptionRegex.MatchString(l.PlainText) || l.IsItalic || l.FontSize <= 0.9*medianFontSize {
					if l.RichXHTML != "" {
						captionRich = l.RichXHTML
					} else {
						var b bytes.Buffer
						escapeXMLText(&b, l.PlainText)
						captionRich = b.String()
					}
					consumedLines[lineIdx] = true
					break
				}
			}
		}

		var figBuf bytes.Buffer
		figBuf.WriteString(`<figure><img src="`)
		escapeXMLAttr(&figBuf, epubPath)
		figBuf.WriteString(`" alt="Figure"/>`)
		if captionRich != "" {
			figBuf.WriteString(`<figcaption>`)
			figBuf.WriteString(captionRich)
			figBuf.WriteString(`</figcaption>`)
		}
		figBuf.WriteString(`</figure>`)

		blocks = append(blocks, ParsedBlock{
			Kind:    BlockFigure,
			XHTML:   figBuf.String(),
			PageNum: pageNum,
			Top:     img.Top,
		})
	}

	// Step B: Vector Diagram Recovery ("The Vector Trap")
	// If a Figure X.Y caption is found but no raster image was nearby, crop vector region!
	for lineIdx, l := range lines {
		if consumedLines[lineIdx] {
			continue
		}
		if figureCaptionRegex.MatchString(l.PlainText) {
			prevBottom := 0.0
			if lineIdx > 0 {
				prevBottom = lines[lineIdx-1].Top + lines[lineIdx-1].Height
			}
			gap := l.Top - prevBottom
			if gap >= 70.0 && config.CropRegion != nil {
				cropTop := prevBottom + 5.0
				cropHeight := gap - 10.0
				cropLeft := math.Max(20.0, l.Left-20.0)
				cropWidth := math.Min(560.0, l.Width+40.0)

				imgPath, err := config.CropRegion(pageNum, cropLeft, cropTop, cropWidth, cropHeight, fmt.Sprintf("diagram_p%03d_%02d", pageNum, lineIdx))
				if err == nil {
					consumedLines[lineIdx] = true
					var figBuf bytes.Buffer
					figBuf.WriteString(`<figure><img src="`)
					escapeXMLAttr(&figBuf, imgPath)
					figBuf.WriteString(`" alt="Diagram"/>`)
					figBuf.WriteString(`<figcaption>`)
					if l.RichXHTML != "" {
						figBuf.WriteString(l.RichXHTML)
					} else {
						escapeXMLText(&figBuf, l.PlainText)
					}
					figBuf.WriteString(`</figcaption></figure>`)

					blocks = append(blocks, ParsedBlock{
						Kind:    BlockFigure,
						XHTML:   figBuf.String(),
						PageNum: pageNum,
						Top:     cropTop,
					})
				}
			}
		}
	}

	// Step C: Main Line Sweep (Tables, Code Blocks, Headings, Scene Breaks, Lists, Paragraphs)
	lineIdx := 0
	for lineIdx < len(lines) {
		if consumedLines[lineIdx] {
			lineIdx++
			continue
		}

		line := lines[lineIdx]

		// 1. Scene Break (<hr class="scene-break"/>)
		if isSceneBreakLine(line.PlainText) {
			blocks = append(blocks, ParsedBlock{
				Kind:    BlockSceneBreak,
				XHTML:   `<hr class="scene-break"/>`,
				PageNum: pageNum,
				Top:     line.Top,
			})
			consumedLines[lineIdx] = true
			lineIdx++
			continue
		}

		// 2. Tables (Simple or Complex)
		if line.HasMultipleColumns || tableCaptionRegex.MatchString(line.PlainText) {
			tableLines, caption, nextIdx := collectTableLines(lines, lineIdx, config.MedianPitch)
			if len(tableLines) >= 2 {
				xhtml := renderTableBlock(tableLines, caption, pageNum, config)
				blocks = append(blocks, ParsedBlock{
					Kind:    BlockTable,
					XHTML:   xhtml,
					PageNum: pageNum,
					Top:     tableLines[0].Top,
				})
				for k := lineIdx; k < nextIdx; k++ {
					consumedLines[k] = true
				}
				lineIdx = nextIdx
				continue
			}
		}

		// 3. TOC Chapter Numbers (e.g. ONE, TWO ... THIRTY on Contents pages)
		if tocChapterNumRegex.MatchString(strings.TrimSpace(line.PlainText)) {
			isTOC := false
			for k := lineIdx + 1; k < min(len(lines), lineIdx+3); k++ {
				if strings.Contains(lines[k].PlainText, "·") || strings.Contains(lines[k].RichXHTML, "<a ") {
					isTOC = true
					break
				}
			}
			if isTOC {
				var b bytes.Buffer
				b.WriteString(`<p class="toc-chapter-num"><strong>`)
				escapeXMLText(&b, strings.TrimSpace(line.PlainText))
				b.WriteString(`</strong></p>`)
				blocks = append(blocks, ParsedBlock{
					Kind:    BlockParagraph,
					XHTML:   b.String(),
					PageNum: pageNum,
					Top:     line.Top,
				})
				consumedLines[lineIdx] = true
				lineIdx++
				continue
			}
		}

		// 4. Headings (h1, h2, h3)
		if isHeading1(line, medianFontSize) {
			title := cleanHeadingTitle(line.PlainText)
			hContent := line.RichXHTML
			if hContent == "" {
				var hBuf bytes.Buffer
				escapeXMLText(&hBuf, title)
				hContent = hBuf.String()
			}
			blocks = append(blocks, ParsedBlock{
				Kind:    BlockHeading1,
				XHTML:   fmt.Sprintf("<h1>%s</h1>", hContent),
				Title:   title,
				PageNum: pageNum,
				Top:     line.Top,
			})
			consumedLines[lineIdx] = true
			lineIdx++
			continue
		}

		if isHeading2(line, medianFontSize) {
			title := cleanHeadingTitle(line.PlainText)
			hContent := line.RichXHTML
			if hContent == "" {
				var hBuf bytes.Buffer
				escapeXMLText(&hBuf, title)
				hContent = hBuf.String()
			}
			blocks = append(blocks, ParsedBlock{
				Kind:    BlockHeading2,
				XHTML:   fmt.Sprintf("<h2>%s</h2>", hContent),
				Title:   title,
				PageNum: pageNum,
				Top:     line.Top,
			})
			consumedLines[lineIdx] = true
			lineIdx++
			continue
		}

		if isHeading3(lines, lineIdx, medianFontSize, config.MedianPitch) {
			title := cleanHeadingTitle(line.PlainText)
			hContent := line.RichXHTML
			if hContent == "" {
				var hBuf bytes.Buffer
				escapeXMLText(&hBuf, title)
				hContent = hBuf.String()
			}
			blocks = append(blocks, ParsedBlock{
				Kind:    BlockHeading3,
				XHTML:   fmt.Sprintf("<h3>%s</h3>", hContent),
				Title:   title,
				PageNum: pageNum,
				Top:     line.Top,
			})
			consumedLines[lineIdx] = true
			lineIdx++
			continue
		}

		// 5. Code Block or ASCII Diagram
		if isCodeStart(line, config.Profile) {
			codeLines, nextIdx := collectCodeBlock(lines, lineIdx, config.MedianPitch, config.Profile)
			xhtml := renderCodeBlock(codeLines)
			blocks = append(blocks, ParsedBlock{
				Kind:    BlockCode,
				XHTML:   xhtml,
				PageNum: pageNum,
				Top:     codeLines[0].Top,
			})
			for k := lineIdx; k < nextIdx; k++ {
				consumedLines[k] = true
			}
			lineIdx = nextIdx
			continue
		}

		// 6. Lists (Unordered or Ordered)
		if match := listItemRegex.FindStringSubmatch(line.PlainText); len(match) > 1 {
			listItems, nextIdx := collectListBlock(lines, lineIdx, config.MedianPitch)
			xhtml := renderListBlock(listItems)
			blocks = append(blocks, ParsedBlock{
				Kind:    BlockList,
				XHTML:   xhtml,
				PageNum: pageNum,
				Top:     line.Top,
			})
			for k := lineIdx; k < nextIdx; k++ {
				consumedLines[k] = true
			}
			lineIdx = nextIdx
			continue
		}

		// 7. Body Paragraph (with Fusion, Dialogue Separation, Drop Cap, Lexicon De-hyphenation)
		paraLines, nextIdx := collectParagraphLines(lines, lineIdx, medianFontSize, config)
		xhtml := renderParagraphBlock(paraLines, medianFontSize, config)
		blocks = append(blocks, ParsedBlock{
			Kind:    BlockParagraph,
			XHTML:   xhtml,
			PageNum: pageNum,
			Top:     paraLines[0].Top,
		})
		for k := lineIdx; k < nextIdx; k++ {
			consumedLines[k] = true
		}
		lineIdx = nextIdx
	}

	// Sort blocks on the page by vertical Top position
	sort.SliceStable(blocks, func(i, j int) bool {
		return blocks[i].Top < blocks[j].Top
	})

	// Coalesce multi-line headings, dedication, and heading hierarchy
	var coalesced []ParsedBlock
	sawParagraph := false

	for i := 0; i < len(blocks); i++ {
		b := blocks[i]
		if b.Kind == BlockParagraph {
			sawParagraph = true
		}

		// Downgrade any Heading1 appearing after the first paragraph to Heading2
		if sawParagraph && b.Kind == BlockHeading1 {
			b.Kind = BlockHeading2
			b.XHTML = strings.Replace(b.XHTML, "<h1>", "<h2>", 1)
			b.XHTML = strings.Replace(b.XHTML, "</h1>", "</h2>", 1)
		}

		if len(coalesced) > 0 {
			prev := &coalesced[len(coalesced)-1]
			// Case A: Consecutive Heading1/Heading2 lines
			if (prev.Kind == BlockHeading1 || prev.Kind == BlockHeading2) && (b.Kind == BlockHeading1 || b.Kind == BlockHeading2) {
				// Check dedication (e.g. Page 8: "To Mackenzie, / My beautiful daughter...")
				isDedication := strings.HasPrefix(strings.ToLower(prev.Title), "to ") ||
					strings.Contains(strings.ToLower(prev.Title), "dedicate") ||
					strings.Contains(strings.ToLower(b.Title), "dedicate")
				if isDedication {
					prev.Kind = BlockParagraph
					prevClean := stripHeadingTags(prev.XHTML)
					prevClean = strings.TrimSuffix(strings.TrimPrefix(prevClean, "<p class=\"dedication\">"), "</p>")
					currClean := stripHeadingTags(b.XHTML)
					prev.XHTML = fmt.Sprintf("<p class=\"dedication\">%s<br/>%s</p>", prevClean, currClean)
					prev.Title = ""
					continue
				}

				// Otherwise coalesce consecutive heading lines into a single heading
				prevText := stripHeadingTags(prev.XHTML)
				currText := stripHeadingTags(b.XHTML)
				targetKind := prev.Kind
				tag := "h1"
				if targetKind == BlockHeading2 {
					tag = "h2"
				}
				prev.Kind = targetKind
				prev.XHTML = fmt.Sprintf("<%s>%s %s</%s>", tag, prevText, currText, tag)
				prev.Title = fmt.Sprintf("%s %s", prev.Title, b.Title)
				continue
			}

			// Case B: Heading1 followed by Figure followed by Heading1 (chapter illustration placed between chapter number and title)
			if b.Kind == BlockHeading1 && len(coalesced) >= 2 && coalesced[len(coalesced)-2].Kind == BlockHeading1 && prev.Kind == BlockFigure {
				b.Kind = BlockHeading2
				b.XHTML = strings.Replace(b.XHTML, "<h1>", "<h2>", 1)
				b.XHTML = strings.Replace(b.XHTML, "</h1>", "</h2>", 1)
			}
		}

		coalesced = append(coalesced, b)
	}

	return coalesced
}

func isHeading1(l VisualLine, medianFontSize float64) bool {
	if l.HasMultipleColumns || len(l.CellStops) >= 2 {
		return false
	}
	trimmed := strings.TrimSpace(l.PlainText)
	if len(trimmed) <= 1 || len(trimmed) > 90 {
		return false
	}
	// Multi-sentence text or letters are paragraphs, not headings
	if strings.Count(trimmed, ". ") >= 1 || strings.Count(trimmed, "; ") >= 2 {
		return false
	}
	if pageNumNoiseRegex.MatchString(trimmed) {
		return false
	}
	if l.PageHeight > 0 && l.Top >= 0.85*l.PageHeight {
		return false
	}
	if l.FontSize >= 1.45*medianFontSize {
		return true
	}
	if heading1Regex.MatchString(l.PlainText) && (l.IsBold || l.FontSize >= 1.2*medianFontSize) {
		return true
	}
	return false
}

func isHeading2(l VisualLine, medianFontSize float64) bool {
	if l.HasMultipleColumns || len(l.CellStops) >= 2 {
		return false
	}
	trimmed := strings.TrimSpace(l.PlainText)
	if len(trimmed) <= 1 {
		return false
	}
	if pageNumNoiseRegex.MatchString(trimmed) {
		return false
	}
	if l.PageHeight > 0 && l.Top >= 0.85*l.PageHeight {
		return false
	}
	return l.FontSize >= 1.22*medianFontSize && l.FontSize < 1.45*medianFontSize
}

func isHeading3(lines []VisualLine, idx int, medianFontSize, medianPitch float64) bool {
	l := lines[idx]
	if l.HasMultipleColumns || len(l.CellStops) >= 2 {
		return false
	}
	trimmed := strings.TrimSpace(l.PlainText)
	if len(trimmed) <= 1 {
		return false
	}
	if !l.IsBold || len(l.PlainText) > 80 {
		return false
	}
	// Preceded by a larger gap
	if idx > 0 {
		prev := lines[idx-1]
		gap := l.Top - (prev.Top + prev.Height)
		if gap < 1.3*medianPitch {
			return false
		}
	}
	// Followed by body text
	if idx+1 < len(lines) {
		next := lines[idx+1]
		if next.FontSize <= medianFontSize*1.15 {
			return true
		}
	}
	return false
}

func cleanHeadingTitle(s string) string {
	s = strings.TrimSpace(s)
	// Remove trailing colons or excess whitespace
	return strings.TrimRight(s, ":")
}

func isCodeStart(l VisualLine, profile BookProfile) bool {
	if l.IsMonospace {
		return true
	}
	if countBoxDrawing(l.PlainText) >= 2 {
		return true
	}
	if profile == ProfileTechnical {
		for _, tok := range codeSyntaxTokens {
			if strings.HasPrefix(strings.TrimSpace(l.PlainText), tok) {
				return true
			}
		}
	}
	return false
}

func collectCodeBlock(lines []VisualLine, startIdx int, medianPitch float64, profile BookProfile) ([]VisualLine, int) {
	var block []VisualLine
	idx := startIdx

	for idx < len(lines) {
		curr := lines[idx]
		if len(block) > 0 {
			prev := block[len(block)-1]
			pitch := curr.Top - prev.Top
			if pitch > 2.5*medianPitch {
				break
			}
		}

		isContinuation := false
		if len(block) > 0 {
			if block[0].IsMonospace {
				isContinuation = curr.IsMonospace && curr.Left >= block[0].Left-10.0 && curr.FontSize <= block[0].FontSize*1.15
			} else {
				isContinuation = curr.Left >= block[0].Left-10.0 && curr.FontSize <= block[0].FontSize*1.15
			}
		}

		if isCodeStart(curr, profile) || isContinuation {
			block = append(block, curr)
			idx++
		} else {
			break
		}
	}

	return block, idx
}

func renderCodeBlock(lines []VisualLine) string {
	if len(lines) == 0 {
		return ""
	}
	minLeft := lines[0].Left
	for _, l := range lines {
		if l.Left < minLeft {
			minLeft = l.Left
		}
	}

	monoCharW := 7.2
	if lines[0].FontSize > 0 {
		monoCharW = lines[0].FontSize * 0.58
	}

	// Compute base pitch between consecutive lines to detect blank lines
	basePitch := 14.0
	var pitches []float64
	for i := 1; i < len(lines); i++ {
		p := lines[i].Top - lines[i-1].Top
		if p > 0 {
			pitches = append(pitches, p)
		}
	}
	if len(pitches) > 0 {
		sort.Float64s(pitches)
		basePitch = pitches[len(pitches)/2]
		if basePitch <= 0 {
			basePitch = 14.0
		}
	}

	var buf bytes.Buffer
	buf.WriteString("<pre><code>")
	for i, l := range lines {
		if i > 0 {
			pitch := l.Top - lines[i-1].Top
			if pitch >= 1.65*basePitch {
				extraLines := int(math.Round(pitch/basePitch)) - 1
				if extraLines > 2 {
					extraLines = 2
				}
				for k := 0; k < extraLines; k++ {
					buf.WriteByte('\n')
				}
			}
		}

		indentFromLeft := int(math.Round((l.Left - minLeft) / monoCharW))
		if indentFromLeft < 0 {
			indentFromLeft = 0
		}
		plain := strings.ReplaceAll(l.PlainText, "\t", "    ")
		trimmedLeft := strings.TrimLeft(plain, " ")
		textSpaces := len(plain) - len(trimmedLeft)

		totalIndent := textSpaces
		if indentFromLeft > textSpaces {
			totalIndent = indentFromLeft
		}
		if totalIndent > 64 {
			totalIndent = 64
		}

		buf.WriteString(strings.Repeat(" ", totalIndent))
		escapeXMLText(&buf, trimmedLeft)
		if i+1 < len(lines) {
			buf.WriteByte('\n')
		}
	}
	buf.WriteString("</code></pre>")
	return buf.String()
}

func collectTableLines(lines []VisualLine, startIdx int, medianPitch float64) ([]VisualLine, string, int) {
	var tableLines []VisualLine
	caption := ""
	idx := startIdx

	if tableCaptionRegex.MatchString(lines[idx].PlainText) {
		caption = lines[idx].PlainText
		idx++
	}

	for idx < len(lines) {
		curr := lines[idx]
		if len(tableLines) > 0 {
			prev := tableLines[len(tableLines)-1]
			gap := curr.Top - (prev.Top + prev.Height)
			if gap > 2.2*medianPitch {
				break
			}
		}

		if curr.HasMultipleColumns || (len(tableLines) > 0 && len(curr.CellStops) >= 2) {
			tableLines = append(tableLines, curr)
			idx++
		} else {
			break
		}
	}

	return tableLines, caption, idx
}

func renderTableBlock(rows []VisualLine, caption string, pageNum int, config StateMachineConfig) string {
	if len(rows) == 0 {
		return ""
	}

	maxCols := 0
	for _, r := range rows {
		if len(r.CellStops) > maxCols {
			maxCols = len(r.CellStops)
		}
	}

	// If complex (> 3 columns or > 30 rows), crop as high-DPI image
	if (maxCols > 3 || len(rows) > 30) && config.CropRegion != nil {
		minLeft := rows[0].Left
		maxRight := rows[0].Right
		minTop := rows[0].Top
		maxBottom := rows[len(rows)-1].Top + rows[len(rows)-1].Height

		for _, r := range rows {
			if r.Left < minLeft {
				minLeft = r.Left
			}
			if r.Right > maxRight {
				maxRight = r.Right
			}
		}

		imgPath, err := config.CropRegion(pageNum, minLeft, minTop, maxRight-minLeft, maxBottom-minTop, fmt.Sprintf("table_p%03d_%02d", pageNum, int(rows[0].Top)))
		if err == nil {
			var figBuf bytes.Buffer
			figBuf.WriteString(`<figure class="table-figure"><img src="`)
			escapeXMLAttr(&figBuf, imgPath)
			figBuf.WriteString(`" alt="Table"/>`)
			if caption != "" {
				figBuf.WriteString(`<figcaption>`)
				escapeXMLText(&figBuf, caption)
				figBuf.WriteString(`</figcaption>`)
			}
			figBuf.WriteString(`</figure>`)
			return figBuf.String()
		}
	}

	// Simple Table HTML
	var buf bytes.Buffer
	buf.WriteString(`<div class="table-wrap"><table>`)
	if caption != "" {
		buf.WriteString(`<caption>`)
		escapeXMLText(&buf, caption)
		buf.WriteString(`</caption>`)
	}

	for rowIdx, r := range rows {
		buf.WriteString(`<tr>`)
		isHeader := rowIdx == 0 && r.IsBold
		cellTag := "td"
		if isHeader {
			cellTag = "th"
		}
		for _, cell := range r.CellStops {
			buf.WriteString(`<` + cellTag + `>`)
			if cell.RichXHTML != "" {
				buf.WriteString(cell.RichXHTML)
			} else {
				escapeXMLText(&buf, cell.PlainText)
			}
			buf.WriteString(`</` + cellTag + `>`)
		}
		buf.WriteString(`</tr>`)
	}
	buf.WriteString(`</table></div>`)
	return buf.String()
}

type listItem struct {
	Text     string
	RichText string
}

func collectListBlock(lines []VisualLine, startIdx int, medianPitch float64) ([]listItem, int) {
	var items []listItem
	idx := startIdx

	for idx < len(lines) {
		curr := lines[idx]
		m := listItemRegex.FindStringSubmatch(curr.PlainText)
		if len(m) > 1 {
			contentPlain := m[1]
			contentRich := curr.RichXHTML
			if contentRich == "" {
				var b bytes.Buffer
				escapeXMLText(&b, contentPlain)
				contentRich = b.String()
			} else {
				contentRich = strings.TrimLeft(contentRich, " \t\u2022\u25e6\u25aa\u2013*-0123456789.)")
			}
			items = append(items, listItem{Text: contentPlain, RichText: contentRich})
			idx++
		} else if len(items) > 0 {
			// Check if continuation of active list item
			prev := lines[idx-1]
			pitch := curr.Top - prev.Top
			if pitch <= 1.4*medianPitch && curr.Left >= prev.Left+10.0 {
				items[len(items)-1].Text += " " + curr.PlainText
				if curr.RichXHTML != "" {
					items[len(items)-1].RichText += " " + curr.RichXHTML
				} else {
					var b bytes.Buffer
					escapeXMLText(&b, curr.PlainText)
					items[len(items)-1].RichText += " " + b.String()
				}
				idx++
			} else {
				break
			}
		} else {
			break
		}
	}

	return items, idx
}

func renderListBlock(items []listItem) string {
	var buf bytes.Buffer
	buf.WriteString("<ul>")
	for _, it := range items {
		buf.WriteString("<li>")
		if it.RichText != "" {
			buf.WriteString(it.RichText)
		} else {
			escapeXMLText(&buf, it.Text)
		}
		buf.WriteString("</li>")
	}
	buf.WriteString("</ul>")
	return buf.String()
}

func collectParagraphLines(lines []VisualLine, startIdx int, medianFontSize float64, config StateMachineConfig) ([]VisualLine, int) {
	var para []VisualLine
	para = append(para, lines[startIdx])
	idx := startIdx + 1

	firstTrimmed := strings.TrimSpace(lines[startIdx].PlainText)
	isDropCapLead := len(firstTrimmed) == 1 && unicode.IsUpper(rune(firstTrimmed[0]))

	for idx < len(lines) {
		curr := lines[idx]
		prev := para[len(para)-1]

		// Discontinue if special state detected
		if isHeading1(curr, medianFontSize) || isHeading2(curr, medianFontSize) || isCodeStart(curr, config.Profile) ||
			curr.HasMultipleColumns || listItemRegex.MatchString(curr.PlainText) ||
			isSceneBreakLine(curr.PlainText) || figureCaptionRegex.MatchString(curr.PlainText) {
			break
		}

		// Dialogue line starts fresh paragraph (unless curr is immediately following a single drop cap lead)
		if dialogueLeadRegex.MatchString(strings.TrimSpace(curr.PlainText)) && !isDropCapLead {
			break
		}

		// Vertical pitch check
		pitch := curr.Top - prev.Top
		maxPitch := 1.6 * config.MedianPitch
		if prev.IsItalic && curr.IsItalic && math.Abs(prev.FontSize-curr.FontSize) <= 1.5 {
			maxPitch = 2.4 * config.MedianPitch
		}
		if pitch < 0.7*config.MedianPitch || pitch > maxPitch {
			// Allow drop cap lead to connect with its paragraph even if slightly offset
			if !isDropCapLead || pitch > 2.5*config.MedianPitch {
				break
			}
		}

		// Paragraph fusion: check right boundary of previous line
		// If prev line does not reach right margin and ends with terminal punctuation, it's a paragraph end
		bodyWidth := 480.0
		if prev.PageWidth > 0 {
			bodyWidth = prev.PageWidth * 0.72
		}
		endsPunct := endsWithSentencePunct(prev.PlainText)
		reachesRight := prev.Right >= (prev.PageWidth*0.82) || prev.Width >= 0.75*bodyWidth

		if endsPunct && !reachesRight && !isDropCapLead {
			// Definite end of paragraph
			break
		}

		// Indentation check: if next line is significantly indented, it starts a new paragraph
		charW := math.Max(5.0, curr.Height*0.52)
		if curr.Left > prev.Left+1.8*charW && endsPunct && !isDropCapLead {
			break
		}

		para = append(para, curr)
		isDropCapLead = false
		idx++
	}

	return para, idx
}

func endsWithSentencePunct(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	runes := []rune(s)
	last := runes[len(runes)-1]
	if last == '.' || last == '!' || last == '?' || last == ':' || last == '”' || last == '"' || last == '’' || last == '\'' || last == '…' {
		return true
	}
	if len(runes) >= 2 {
		prev := runes[len(runes)-2]
		if (last == '"' || last == '”' || last == '\'' || last == '’') && (prev == '.' || prev == '!' || prev == '?' || prev == ',') {
			return true
		}
	}
	return false
}

func fuseParagraphLines(lines []VisualLine, lex *DocumentLexicon) string {
	if len(lines) == 0 {
		return ""
	}
	var textParts []string
	var richParts []string

	for _, l := range lines {
		textParts = append(textParts, l.PlainText)
		if l.RichXHTML != "" {
			richParts = append(richParts, l.RichXHTML)
		} else {
			var b bytes.Buffer
			escapeXMLText(&b, l.PlainText)
			richParts = append(richParts, b.String())
		}
	}

	var fusedRich strings.Builder
	for i := 0; i < len(lines); i++ {
		currRich := richParts[i]
		currPlain := textParts[i]

		if i == 0 {
			fusedRich.WriteString(currRich)
			continue
		}

		prevPlain := textParts[i-1]
		if strings.HasSuffix(prevPlain, "-") && lex != nil {
			prevWords := strings.Fields(prevPlain)
			currWords := strings.Fields(currPlain)
			if len(prevWords) > 0 && len(currWords) > 0 {
				w1 := prevWords[len(prevWords)-1]
				w2 := currWords[0]
				resolved := lex.Dehyphenate(w1, w2)

				// Adjust the fused output: replace trailing w1- and leading w2 with resolved
				fusedStr := fusedRich.String()
				trimIdx := strings.LastIndex(fusedStr, w1)
				if trimIdx != -1 {
					fusedRich.Reset()
					fusedRich.WriteString(fusedStr[:trimIdx])
					var b bytes.Buffer
					escapeXMLText(&b, resolved)
					fusedRich.WriteString(b.String())

					// Append remaining words of currRich
					currWordsOnly := strings.SplitN(currRich, w2, 2)
					if len(currWordsOnly) > 1 {
						fusedRich.WriteString(currWordsOnly[1])
					}
					continue
				}
			}
		}

		fusedRich.WriteByte(' ')
		fusedRich.WriteString(currRich)
	}
	return fusedRich.String()
}

func renderParagraphBlock(lines []VisualLine, medianFontSize float64, config StateMachineConfig) string {
	if len(lines) == 0 {
		return ""
	}

	// Case 1: First line is a single uppercase letter drop cap
	if len(lines) >= 2 {
		firstTrimmed := strings.TrimSpace(lines[0].PlainText)
		if len(firstTrimmed) == 1 && unicode.IsUpper(rune(firstTrimmed[0])) {
			r := rune(firstTrimmed[0])
			rest := fuseParagraphLines(lines[1:], config.Lexicon)
			restTrimmed := strings.TrimLeft(rest, " ")
			sep := ""
			var firstRuneRest rune
			for _, ch := range restTrimmed {
				if unicode.IsLetter(ch) {
					firstRuneRest = ch
					break
				}
			}
			if (r == 'A' || r == 'I') && unicode.IsUpper(firstRuneRest) {
				sep = " "
			}
			return fmt.Sprintf(`<p><span class="dropcap">%c</span>%s%s</p>`, r, sep, restTrimmed)
		}
	}

	content := fuseParagraphLines(lines, config.Lexicon)

	// Case 2: Drop Cap single letter inside first line (must span multiple lines and not be an initial like "J. K.")
	firstLine := lines[0]
	if len(lines) >= 2 && firstLine.FontSize >= 1.7*medianFontSize && len(firstLine.PlainText) > 0 && unicode.IsUpper(rune(firstLine.PlainText[0])) {
		r := rune(firstLine.PlainText[0])
		rest := strings.TrimPrefix(content, string(r))
		restTrimmed := strings.TrimSpace(rest)
		if !strings.HasPrefix(restTrimmed, ".") && !strings.HasPrefix(restTrimmed, ",") {
			var dropBuf bytes.Buffer
			dropBuf.WriteString(`<p><span class="dropcap">`)
			dropBuf.WriteRune(r)
			dropBuf.WriteString(`</span>`)
			dropBuf.WriteString(rest)
			dropBuf.WriteString(`</p>`)
			return dropBuf.String()
		}
	}

	return "<p>" + content + "</p>"
}

func median(values []float64, fallback float64) float64 {
	if len(values) == 0 {
		return fallback
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2.0
	}
	return sorted[mid]
}
