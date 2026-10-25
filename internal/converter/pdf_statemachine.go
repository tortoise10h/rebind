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
	pageNumNoiseRegex     = regexp.MustCompile(`^[\s\p{P}\p{S}\x{0091}\x{0092}\d]+$`)
	tocChapterNumRegex    = regexp.MustCompile(`(?i)^(?:one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|twenty-one|twenty-two|twenty-three|twenty-four|twenty-five|twenty-six|twenty-seven|twenty-eight|twenty-nine|thirty)$`)
	techCaptionRegex      = regexp.MustCompile(`^(?:Figure|FIGURE|Fig\.|FIG\.|Table|TABLE|Listing|LISTING|Algorithm|ALGORITHM)\s+\d+`)
	tableCaptionRegex     = regexp.MustCompile(`^(?:Table|TABLE)\s+\d+`)
	listingCaptionRegex   = regexp.MustCompile(`^(?:Listing|LISTING)\s+\d+`)
	figureCaptionRegex    = regexp.MustCompile(`^(?:Figure|FIGURE|Fig\.|FIG\.|Diagram|DIAGRAM|Chart|CHART|Plate|PLATE)\s+\d+`)
	heading1Regex         = regexp.MustCompile(`(?i)^(?:chapter|part|book|prologue|epilogue)\s+([0-9]+|[ivxlcdm]+|one|two|three|four|five|six|seven|eight|nine|ten)\b`)
	sectionH2Regex        = regexp.MustCompile(`^(?:[0-9]+\.[0-9]+|[A-Z]\.[0-9]+)\s+\S`)
	sectionH3Regex        = regexp.MustCompile(`^(?:[0-9]+\.[0-9]+\.[0-9]+(?:\.[0-9]+)*|[A-Z]\.[0-9]+\.[0-9]+)\s+\S`)
	listItemRegex         = regexp.MustCompile(`^(?:[•◦▪–*\-]|\d+[\.\)]|\([a-zA-Z0-9]+\))\s+(.*)$`)
	orderedItemPlainRegex = regexp.MustCompile(`^(?:\d+[\.\)]|\([a-zA-Z0-9]+\))\s+(.*)$`)
	boldStepItemRegex     = regexp.MustCompile(`^<strong>(\d{1,2})</strong>\s+(.*)$`)
	dialogueLeadRegex     = regexp.MustCompile(`^["“‘—–«']`)
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

type normImage struct {
	OrigIdx  int
	Src      string
	Left     float64
	Top      float64
	Width    float64
	Height   float64
	Right    float64
	Bottom   float64
	FlippedY bool
}

func isFigureCalloutOrDiagramLine(lines []VisualLine, idx int, medianFontSize float64) bool {
	l := lines[idx]
	trimmed := strings.TrimSpace(l.PlainText)
	if trimmed == "" {
		return false
	}
	if techCaptionRegex.MatchString(trimmed) {
		return false
	}
	if isHeading1(l, medianFontSize) || isHeading2(l, medianFontSize) || sectionH3Regex.MatchString(trimmed) {
		return false
	}
	if listItemRegex.MatchString(trimmed) || boldStepItemRegex.MatchString(l.RichXHTML) {
		return false
	}
	if strings.HasPrefix(trimmed, "NOTE ") || strings.HasPrefix(trimmed, "TIP ") || strings.HasPrefix(trimmed, "WARNING ") {
		return false
	}
	if l.IsMonospace && !l.IsBold {
		return false
	}
	return (l.IsBold && l.FontSize <= 1.05*medianFontSize) || l.FontSize <= 0.85*medianFontSize || l.HasMultipleColumns
}

func hasCaptionBetween(lines []VisualLine, y1, y2 float64) bool {
	if y2 < y1 {
		y1, y2 = y2, y1
	}
	for _, l := range lines {
		if l.Top >= y1-2.0 && l.Top <= y2+2.0 && figureCaptionRegex.MatchString(strings.TrimSpace(l.PlainText)) {
			return true
		}
	}
	return false
}

func parseSinglePage(lines []VisualLine, images []xmlImage, pageNum int, medianFontSize float64, config StateMachineConfig) []ParsedBlock {
	var blocks []ParsedBlock
	consumedLines := make(map[int]bool)
	consumedImages := make(map[int]bool)

	// Normalize image coordinates (Poppler emits negative height when PDF CTM flips Y axis)
	normImgs := make([]normImage, 0, len(images))
	for i, img := range images {
		top := img.Top
		h := img.Height
		flipped := false
		if h < 0 {
			top = img.Top + img.Height
			h = -img.Height
			flipped = true
		} else if h == 0 {
			h = 1.0
		}
		w := img.Width
		if w <= 0 {
			w = 1.0
		}
		normImgs = append(normImgs, normImage{
			OrigIdx:  i,
			Src:      img.Src,
			Left:     img.Left,
			Top:      top,
			Width:    w,
			Height:   h,
			Right:    img.Left + w,
			Bottom:   top + h,
			FlippedY: flipped,
		})
	}

	sort.SliceStable(normImgs, func(i, j int) bool {
		if math.Abs(normImgs[i].Top-normImgs[j].Top) > 2.0 {
			return normImgs[i].Top < normImgs[j].Top
		}
		return normImgs[i].Left < normImgs[j].Left
	})

	// Step A1: Group vertically stacked micro-strips (e.g. Page 7's 231 1px slices)
	for i := 0; i < len(normImgs); i++ {
		if consumedImages[normImgs[i].OrigIdx] {
			continue
		}
		curr := normImgs[i]
		group := []normImage{curr}
		minLeft := curr.Left
		minTop := curr.Top
		maxRight := curr.Right
		maxBottom := curr.Bottom

		for j := i + 1; j < len(normImgs); j++ {
			if consumedImages[normImgs[j].OrigIdx] {
				continue
			}
			next := normImgs[j]
			overlap := math.Min(maxRight, next.Right) - math.Max(minLeft, next.Left)
			isMicro := curr.Height <= 8.0 || next.Height <= 8.0 || (len(group) > 1 && next.Height <= 25.0)
			if isMicro && math.Abs(next.Top-maxBottom) <= 4.0 && overlap > 20.0 {
				group = append(group, next)
				if next.Left < minLeft {
					minLeft = next.Left
				}
				if next.Top < minTop {
					minTop = next.Top
				}
				if next.Right > maxRight {
					maxRight = next.Right
				}
				if next.Bottom > maxBottom {
					maxBottom = next.Bottom
				}
			} else if next.Top > maxBottom+20.0 {
				break
			}
		}

		if len(group) >= 4 && config.CropRegion != nil && (maxBottom-minTop) <= 80.0 {
			for _, g := range group {
				consumedImages[g.OrigIdx] = true
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

	// Step A2: Coalesce overlapping/adjacent images (e.g. figure + border strips) and process figures
	type compositeImageGroup struct {
		Imgs     []normImage
		Left     float64
		Top      float64
		Right    float64
		Bottom   float64
		FlippedY bool
	}
	var imgGroups []compositeImageGroup
	for _, ni := range normImgs {
		if consumedImages[ni.OrigIdx] {
			continue
		}
		placed := false
		for gIdx := range imgGroups {
			g := &imgGroups[gIdx]
			hOverlap := math.Min(g.Right, ni.Right) - math.Max(g.Left, ni.Left)
			vTouching := ni.Top <= g.Bottom+8.0 && ni.Bottom >= g.Top-8.0
			if hOverlap >= -5.0 && vTouching && !hasCaptionBetween(lines, g.Bottom, ni.Top) {
				g.Imgs = append(g.Imgs, ni)
				if ni.Left < g.Left {
					g.Left = ni.Left
				}
				if ni.Top < g.Top {
					g.Top = ni.Top
				}
				if ni.Right > g.Right {
					g.Right = ni.Right
				}
				if ni.Bottom > g.Bottom {
					g.Bottom = ni.Bottom
				}
				if ni.FlippedY {
					g.FlippedY = true
				}
				placed = true
				break
			}
		}
		if !placed {
			imgGroups = append(imgGroups, compositeImageGroup{
				Imgs:     []normImage{ni},
				Left:     ni.Left,
				Top:      ni.Top,
				Right:    ni.Right,
				Bottom:   ni.Bottom,
				FlippedY: ni.FlippedY,
			})
		}
	}

	for gIdx, g := range imgGroups {
		for _, ni := range g.Imgs {
			consumedImages[ni.OrigIdx] = true
		}
		w := g.Right - g.Left
		h := g.Bottom - g.Top

		// Skip full-bleed cover/flap images on pages 1-3 if there's no body text
		isCoverPage := pageNum <= 3 && w >= 300 && h >= 500 && len(lines) <= 1
		if isCoverPage {
			continue
		}

		// Find caption below (up to 125pt if only callouts intervene) or above (up to 45pt)
		captionRich := ""
		captionIdx := -1
		for lineIdx, l := range lines {
			if consumedLines[lineIdx] {
				continue
			}
			distBelow := l.Top - g.Bottom
			distAbove := g.Top - (l.Top + l.Height)
			if distBelow >= -10.0 && distBelow <= 125.0 && figureCaptionRegex.MatchString(strings.TrimSpace(l.PlainText)) {
				// Verify no unconsumed prose/heading line sits between g.Bottom and l.Top
				blocked := false
				for k, mid := range lines {
					if consumedLines[k] || k == lineIdx {
						continue
					}
					if mid.Top >= g.Bottom-2.0 && mid.Top < l.Top-2.0 && !isFigureCalloutOrDiagramLine(lines, k, medianFontSize) {
						blocked = true
						break
					}
				}
				if !blocked {
					captionIdx = lineIdx
					break
				}
			} else if (distBelow >= -5.0 && distBelow <= 45.0) || (distAbove >= -5.0 && distAbove <= 45.0) {
				if figureCaptionRegex.MatchString(l.PlainText) || (config.Profile == ProfileFiction && (l.IsItalic || l.FontSize <= 0.9*medianFontSize)) {
					captionIdx = lineIdx
					break
				}
			}
		}

		captionTop := g.Bottom + 45.0
		if captionIdx >= 0 {
			l := lines[captionIdx]
			if l.RichXHTML != "" {
				captionRich = l.RichXHTML
			} else {
				var b bytes.Buffer
				escapeXMLText(&b, l.PlainText)
				captionRich = b.String()
			}
			consumedLines[captionIdx] = true
			if l.Top > g.Bottom {
				captionTop = l.Top
			}
		}

		// Absorb figure callout annotations inside or immediately adjacent to the figure bounds
		var calloutIdxs []int
		cropLeft := g.Left
		cropTop := g.Top
		cropRight := g.Right
		cropBottom := g.Bottom

		// Scan lines inside [g.Top, captionTop] and immediately above g.Top (up to 95pt if contiguous callouts)
		minAllowedTop := g.Top - 95.0
		for k := len(lines) - 1; k >= 0; k-- {
			if consumedLines[k] {
				continue
			}
			lk := lines[k]
			if lk.Top+lk.Height <= g.Top+2.0 && lk.Top >= g.Top-95.0 {
				if !isFigureCalloutOrDiagramLine(lines, k, medianFontSize) {
					if lk.Top+lk.Height > minAllowedTop {
						minAllowedTop = lk.Top + lk.Height + 2.0
					}
				}
			}
		}

		for lineIdx, l := range lines {
			if consumedLines[lineIdx] {
				continue
			}
			if !isFigureCalloutOrDiagramLine(lines, lineIdx, medianFontSize) {
				continue
			}
			inVerticalSpan := l.Top >= minAllowedTop && (l.Top+l.Height) <= captionTop+2.0
			if inVerticalSpan {
				calloutIdxs = append(calloutIdxs, lineIdx)
				consumedLines[lineIdx] = true
				if l.Left < cropLeft {
					cropLeft = l.Left
				}
				if l.Top < cropTop {
					cropTop = l.Top
				}
				if l.Right > cropRight {
					cropRight = l.Right
				}
				if l.Top+l.Height > cropBottom {
					cropBottom = l.Top + l.Height
				}
			}
		}

		var epubPath string
		var err error
		needsRegionCrop := len(g.Imgs) > 1 || g.FlippedY || len(calloutIdxs) > 0
		if needsRegionCrop && config.CropRegion != nil {
			padLeft := math.Max(0.0, cropLeft-6.0)
			padTop := math.Max(0.0, cropTop-6.0)
			padRight := cropRight + 6.0
			padBottom := cropBottom + 6.0
			if captionIdx >= 0 && lines[captionIdx].Top > g.Top && padBottom > lines[captionIdx].Top-2.0 {
				padBottom = lines[captionIdx].Top - 2.0
			}
			epubPath, err = config.CropRegion(pageNum, padLeft, padTop, padRight-padLeft, padBottom-padTop, fmt.Sprintf("fig_p%03d_%02d", pageNum, gIdx))
		} else if config.AddImage != nil {
			epubPath, err = config.AddImage(g.Imgs[0].Src)
		}
		if err != nil || epubPath == "" {
			continue
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
			Top:     cropTop,
		})
	}

	// Step B: Vector Diagram Recovery ("The Vector Trap")
	// If a Figure X.Y caption is found but no raster image was nearby, absorb diagram text lines above it and crop!
	for lineIdx, l := range lines {
		if consumedLines[lineIdx] {
			continue
		}
		if figureCaptionRegex.MatchString(strings.TrimSpace(l.PlainText)) && config.CropRegion != nil {
			var diagIdxs []int
			diagLeft := l.Left
			diagRight := l.Right
			diagBottom := l.Top - 4.0
			lastTop := l.Top

			// Walk upward through unconsumed diagram/callout lines
			prevBodyBottom := 65.0
			for k := lineIdx - 1; k >= 0; k-- {
				if consumedLines[k] {
					prevBodyBottom = lines[k].Top + lines[k].Height
					break
				}
				cand := lines[k]
				if isFigureCalloutOrDiagramLine(lines, k, medianFontSize) && (lastTop-(cand.Top+cand.Height)) <= 95.0 {
					diagIdxs = append(diagIdxs, k)
					if cand.Left < diagLeft {
						diagLeft = cand.Left
					}
					if cand.Right > diagRight {
						diagRight = cand.Right
					}
					lastTop = cand.Top
				} else {
					prevBodyBottom = cand.Top + cand.Height
					break
				}
			}

			var cropTop, cropHeight, cropLeft, cropWidth float64
			if len(diagIdxs) > 0 {
				cropTop = math.Max(prevBodyBottom+4.0, lastTop-18.0)
				cropBottom := diagBottom
				cropHeight = cropBottom - cropTop
				cropLeft = math.Max(20.0, math.Min(diagLeft, l.Left)-20.0)
				cropRight := math.Max(diagRight, l.Right) + 25.0
				cropWidth = math.Max(520.0, cropRight-cropLeft)
			} else {
				gap := l.Top - prevBodyBottom
				if gap >= 70.0 {
					cropTop = prevBodyBottom + 5.0
					cropHeight = gap - 10.0
					cropLeft = math.Max(20.0, l.Left-20.0)
					cropWidth = math.Max(520.0, l.Width+40.0)
				}
			}

			if cropHeight >= 45.0 {
				imgPath, err := config.CropRegion(pageNum, cropLeft, cropTop, cropWidth, cropHeight, fmt.Sprintf("diagram_p%03d_%02d", pageNum, lineIdx))
				if err == nil {
					consumedLines[lineIdx] = true
					for _, dIdx := range diagIdxs {
						consumedLines[dIdx] = true
					}
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
			tableLines, caption, nextIdx := collectTableLines(lines, lineIdx, medianFontSize, config.MedianPitch)
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

		// 6. Lists (Unordered or Ordered, including bold step numbers)
		if listItemRegex.MatchString(line.PlainText) || boldStepItemRegex.MatchString(line.RichXHTML) {
			listItems, isOrdered, nextIdx := collectListBlock(lines, lineIdx, config.MedianPitch, config.Lexicon)
			xhtml := renderListBlock(listItems, isOrdered, config.Lexicon)
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

				// Only coalesce headings of the SAME level that are vertically close (e.g. wrapped multi-line title)
				if prev.Kind == b.Kind && (b.Top-prev.Top) <= 85.0 {
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
	if sectionH2Regex.MatchString(trimmed) && !sectionH3Regex.MatchString(trimmed) && (l.IsBold || l.FontSize >= 1.1*medianFontSize) {
		return true
	}
	return l.FontSize >= 1.20*medianFontSize && l.FontSize < 1.45*medianFontSize
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
	if listingCaptionRegex.MatchString(trimmed) {
		return true
	}
	if sectionH3Regex.MatchString(trimmed) && (l.IsBold || l.FontSize >= 1.04*medianFontSize) {
		return true
	}
	if !l.IsBold || len(l.PlainText) > 80 {
		return false
	}
	// Reject sentence-like callouts ending with a period
	if strings.HasSuffix(trimmed, ".") {
		return false
	}
	// Preceded by a larger gap
	if idx > 0 {
		prev := lines[idx-1]
		pitch := l.Top - prev.Top
		if pitch < 1.35*medianPitch {
			return false
		}
	}
	// Followed by body text
	if idx+1 < len(lines) {
		next := lines[idx+1]
		if next.FontSize <= medianFontSize*1.15 && !next.IsBold {
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
		// Skip standalone right-margin bold proportional callout annotations sitting alongside/after code lines
		if len(block) > 0 && block[0].IsMonospace && !curr.IsMonospace && curr.IsBold && curr.Left >= 450.0 {
			prev := block[len(block)-1]
			if (curr.Top - prev.Top) <= 2.8*medianPitch {
				idx++
				continue
			}
		}
		if len(block) > 0 {
			prev := block[len(block)-1]
			pitch := curr.Top - prev.Top
			if pitch > 2.8*medianPitch {
				break
			}
		}

		isContinuation := false
		if len(block) > 0 {
			if block[0].IsMonospace {
				isContinuation = (curr.IsMonospace || strings.HasPrefix(strings.TrimSpace(curr.PlainText), "➥")) &&
					curr.Left >= block[0].Left-10.0 && curr.FontSize <= block[0].FontSize*1.4
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

	var renderedLines []string
	var lineTops []float64
	pendingContinuation := false
	for _, l := range lines {
		indentFromLeft := int(math.Round((l.Left - minLeft) / monoCharW))
		if indentFromLeft < 0 {
			indentFromLeft = 0
		}
		plain := strings.ReplaceAll(l.PlainText, "\t", "    ")
		trimmedLeft := strings.TrimLeft(plain, " ")
		if strings.HasPrefix(trimmedLeft, "➥") && len(renderedLines) > 0 {
			cont := strings.TrimSpace(strings.TrimPrefix(trimmedLeft, "➥"))
			if cont != "" {
				renderedLines[len(renderedLines)-1] += " " + cont
				pendingContinuation = false
			} else {
				pendingContinuation = true
			}
			lineTops[len(lineTops)-1] = l.Top
			continue
		}
		if pendingContinuation && len(renderedLines) > 0 {
			renderedLines[len(renderedLines)-1] += " " + strings.TrimSpace(trimmedLeft)
			lineTops[len(lineTops)-1] = l.Top
			pendingContinuation = false
			continue
		}
		textSpaces := len(plain) - len(trimmedLeft)

		totalIndent := textSpaces
		if indentFromLeft > textSpaces {
			totalIndent = indentFromLeft
		}
		if totalIndent > 64 {
			totalIndent = 64
		}
		renderedLines = append(renderedLines, strings.Repeat(" ", totalIndent)+trimmedLeft)
		lineTops = append(lineTops, l.Top)
	}

	var buf bytes.Buffer
	buf.WriteString("<pre><code>")
	for i, text := range renderedLines {
		if i > 0 {
			pitch := lineTops[i] - lineTops[i-1]
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
		escapeXMLText(&buf, text)
		if i+1 < len(renderedLines) {
			buf.WriteByte('\n')
		}
	}
	buf.WriteString("</code></pre>")
	return buf.String()
}

func collectTableLines(lines []VisualLine, startIdx int, medianFontSize, medianPitch float64) ([]VisualLine, string, int) {
	var tableLines []VisualLine
	caption := ""
	idx := startIdx

	if tableCaptionRegex.MatchString(lines[idx].PlainText) {
		caption = lines[idx].PlainText
		idx++
	}

	tableMinLeft := 1000.0
	for idx < len(lines) {
		curr := lines[idx]
		if len(tableLines) > 0 {
			prev := tableLines[len(tableLines)-1]
			gap := curr.Top - (prev.Top + prev.Height)
			if gap > 2.2*medianPitch {
				break
			}
		}

		isMultiColRow := curr.HasMultipleColumns || (len(tableLines) > 0 && len(curr.CellStops) >= 2)
		isCellWrapCont := false
		if !isMultiColRow && len(tableLines) > 0 {
			// Single-column wrapped cell continuation inside a table (smaller table font, aligned with a table column)
			if curr.FontSize <= tableLines[0].FontSize*1.06 &&
				curr.FontSize <= 0.90*medianFontSize &&
				curr.Left >= tableMinLeft-8.0 &&
				!techCaptionRegex.MatchString(curr.PlainText) &&
				!isHeading1(curr, medianFontSize) &&
				!isHeading2(curr, medianFontSize) {
				isCellWrapCont = true
			}
		}

		if isMultiColRow || isCellWrapCont {
			if curr.Left < tableMinLeft {
				tableMinLeft = curr.Left
			}
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

	// Determine canonical column anchor X coordinates from rows with maxCols
	colAnchors := make([]float64, maxCols)
	anchorCounts := make([]int, maxCols)
	for _, r := range rows {
		if len(r.CellStops) == maxCols {
			for c, cell := range r.CellStops {
				colAnchors[c] += cell.Left
				anchorCounts[c]++
			}
		}
	}
	for c := 0; c < maxCols; c++ {
		if anchorCounts[c] > 0 {
			colAnchors[c] /= float64(anchorCounts[c])
		}
	}

	type logicalRow struct {
		IsHeader  bool
		CellLines [][]VisualLine
	}
	var logicalRows []logicalRow

	for rIdx, r := range rows {
		cells := r.CellStops
		if len(cells) == 0 {
			cells = []TextCell{{
				Left:      r.Left,
				Right:     r.Right,
				PlainText: r.PlainText,
				RichXHTML: r.RichXHTML,
			}}
		}

		isHeader := rIdx == 0 && (r.IsBold || caption != "")
		startsNewRow := false
		if len(logicalRows) == 0 || rIdx == 1 && logicalRows[0].IsHeader {
			startsNewRow = true
		} else {
			prevR := rows[rIdx-1]
			pitch := r.Top - prevR.Top
			hasCol0 := len(cells) > 0 && (maxCols == 0 || cells[0].Left <= colAnchors[0]+40.0)
			if hasCol0 && pitch >= 19.5 {
				startsNewRow = true
			}
		}

		if startsNewRow {
			lr := logicalRow{
				IsHeader:  isHeader,
				CellLines: make([][]VisualLine, maxCols),
			}
			if len(cells) == maxCols {
				for c, cell := range cells {
					lr.CellLines[c] = append(lr.CellLines[c], VisualLine{
						PlainText: cell.PlainText,
						RichXHTML: cell.RichXHTML,
					})
				}
			} else {
				for _, cell := range cells {
					bestCol := 0
					bestDist := math.Abs(cell.Left - colAnchors[0])
					for c := 1; c < maxCols; c++ {
						d := math.Abs(cell.Left - colAnchors[c])
						if d < bestDist {
							bestDist = d
							bestCol = c
						}
					}
					lr.CellLines[bestCol] = append(lr.CellLines[bestCol], VisualLine{
						PlainText: cell.PlainText,
						RichXHTML: cell.RichXHTML,
					})
				}
			}
			logicalRows = append(logicalRows, lr)
		} else {
			lr := &logicalRows[len(logicalRows)-1]
			for _, cell := range cells {
				bestCol := 0
				bestDist := math.Abs(cell.Left - colAnchors[0])
				for c := 1; c < maxCols; c++ {
					d := math.Abs(cell.Left - colAnchors[c])
					if d < bestDist {
						bestDist = d
						bestCol = c
					}
				}
				lr.CellLines[bestCol] = append(lr.CellLines[bestCol], VisualLine{
					PlainText: cell.PlainText,
					RichXHTML: cell.RichXHTML,
				})
			}
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

	for _, lr := range logicalRows {
		buf.WriteString(`<tr>`)
		cellTag := "td"
		if lr.IsHeader {
			cellTag = "th"
		}
		for c := 0; c < maxCols; c++ {
			buf.WriteString(`<` + cellTag + `>`)
			if len(lr.CellLines[c]) > 0 {
				buf.WriteString(fuseParagraphLines(lr.CellLines[c], config.Lexicon))
			}
			buf.WriteString(`</` + cellTag + `>`)
		}
		buf.WriteString(`</tr>`)
	}
	buf.WriteString(`</table></div>`)
	return buf.String()
}

type listItem struct {
	Lines []VisualLine
}

func parseListMarker(l VisualLine) (matched bool, isOrdered bool, contentPlain string, contentRich string) {
	if m := listItemRegex.FindStringSubmatch(l.PlainText); len(m) > 1 {
		contentPlain = m[1]
		isOrdered = orderedItemPlainRegex.MatchString(l.PlainText)
		if l.RichXHTML == "" {
			var b bytes.Buffer
			escapeXMLText(&b, contentPlain)
			contentRich = b.String()
		} else {
			contentRich = strings.TrimLeft(l.RichXHTML, " \t\u2022\u25e6\u25aa\u2013*-0123456789.)")
		}
		return true, isOrdered, contentPlain, contentRich
	}
	if m := boldStepItemRegex.FindStringSubmatch(strings.TrimSpace(l.RichXHTML)); len(m) > 2 {
		numPrefix := m[1]
		contentRich = strings.TrimSpace(m[2])
		contentPlain = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l.PlainText), numPrefix))
		return true, true, contentPlain, contentRich
	}
	return false, false, "", ""
}

func collectListBlock(lines []VisualLine, startIdx int, medianPitch float64, lex *DocumentLexicon) ([]listItem, bool, int) {
	var items []listItem
	isOrderedList := false
	idx := startIdx
	markerLeft := lines[startIdx].Left

	for idx < len(lines) {
		curr := lines[idx]
		if len(items) > 0 {
			prev := lines[idx-1]
			pitch := curr.Top - prev.Top
			if pitch > 2.1*medianPitch {
				break
			}
		}

		matched, isOrd, contentPlain, contentRich := parseListMarker(curr)
		if matched {
			if len(items) == 0 {
				isOrderedList = isOrd
				markerLeft = curr.Left
			} else if isOrd != isOrderedList || math.Abs(curr.Left-markerLeft) > 8.0 {
				break
			}
			firstLine := curr
			firstLine.PlainText = contentPlain
			firstLine.RichXHTML = contentRich
			items = append(items, listItem{Lines: []VisualLine{firstLine}})
			idx++
		} else if len(items) > 0 {
			prev := lines[idx-1]
			pitch := curr.Top - prev.Top
			trimmedPlain := strings.TrimSpace(curr.PlainText)
			// Stop before exercise sub-labels like "Objective—" or "Tasks:" or code blocks
			if strings.HasPrefix(trimmedPlain, "Objective—") || strings.HasPrefix(trimmedPlain, "Objective ") ||
				trimmedPlain == "Tasks:" || curr.IsMonospace {
				break
			}
			if pitch <= 1.45*medianPitch && curr.Left >= markerLeft+8.0 {
				items[len(items)-1].Lines = append(items[len(items)-1].Lines, curr)
				idx++
			} else {
				break
			}
		} else {
			break
		}
	}

	return items, isOrderedList, idx
}

func renderListBlock(items []listItem, isOrdered bool, lex ...*DocumentLexicon) string {
	var l *DocumentLexicon
	if len(lex) > 0 {
		l = lex[0]
	}
	tag := "ul"
	if isOrdered {
		tag = "ol"
	}
	var buf bytes.Buffer
	buf.WriteString("<" + tag + ">")
	for _, it := range items {
		buf.WriteString("<li>")
		buf.WriteString(fuseParagraphLines(it.Lines, l))
		buf.WriteString("</li>")
	}
	buf.WriteString("</" + tag + ">")
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
		if isHeading1(curr, medianFontSize) || isHeading2(curr, medianFontSize) ||
			isHeading3(lines, idx, medianFontSize, config.MedianPitch) ||
			isCodeStart(curr, config.Profile) ||
			curr.HasMultipleColumns || listItemRegex.MatchString(curr.PlainText) ||
			boldStepItemRegex.MatchString(curr.RichXHTML) ||
			isSceneBreakLine(curr.PlainText) || techCaptionRegex.MatchString(curr.PlainText) {
			break
		}

		// Exercise sub-sections ("Objective—...", "Tasks:") start a new paragraph
		currTrimmed := strings.TrimSpace(curr.PlainText)
		if strings.HasPrefix(currTrimmed, "Objective—") || currTrimmed == "Tasks:" {
			break
		}

		// Dialogue line starts fresh paragraph (unless curr is immediately following a single drop cap lead)
		if dialogueLeadRegex.MatchString(currTrimmed) && !isDropCapLead {
			break
		}

		// Vertical pitch check
		pitch := curr.Top - prev.Top
		maxPitch := 1.38 * config.MedianPitch
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

var (
	splitCodeHyphenRegex = regexp.MustCompile(`<code>([^<]*)-</code>\s*<code>([^<]+)</code>`)
	splitCodeDotRegex    = regexp.MustCompile(`<code>([^<]+)</code>\s+<code>(\.[a-zA-Z0-9_]+)</code>`)
)

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

				fusedStr := fusedRich.String()
				// Handle inline <code>w1-</code> followed by <code>w2...</code>
				if strings.HasSuffix(fusedStr, "</code>") && strings.HasPrefix(currRich, "<code>") {
					codeHyphenSuffix := w1 + "</code>"
					if strings.HasSuffix(fusedStr, codeHyphenSuffix) {
						fusedRich.Reset()
						fusedRich.WriteString(strings.TrimSuffix(fusedStr, codeHyphenSuffix))
						var b bytes.Buffer
						escapeXMLText(&b, resolved)
						fusedRich.WriteString(b.String())
						restCurr := strings.TrimPrefix(currRich, "<code>"+w2)
						fusedRich.WriteString(restCurr)
						continue
					}
				}

				// Adjust the fused output: replace trailing w1- and leading w2 with resolved
				trimIdx := strings.LastIndex(fusedStr, w1)
				if trimIdx != -1 && !strings.Contains(fusedStr[trimIdx:], "<") && !strings.HasPrefix(currRich, "<") {
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
	out := fusedRich.String()
	out = splitCodeHyphenRegex.ReplaceAllString(out, "<code>$1$2</code>")
	out = splitCodeDotRegex.ReplaceAllString(out, "<code>$1$2</code>")
	return out
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
