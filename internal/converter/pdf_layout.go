package converter

import (
	"bytes"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

func stripHTMLTags(s string) string {
	return htmlTagRegex.ReplaceAllString(s, "")
}

// expandUnicodeLigatures replaces typographic ligatures with their standard ASCII equivalents.
func expandUnicodeLigatures(s string) string {
	if s == "" {
		return ""
	}
	// Unicode presentation ligatures
	s = strings.ReplaceAll(s, "\uFB00", "ff")
	s = strings.ReplaceAll(s, "\uFB01", "fi")
	s = strings.ReplaceAll(s, "\uFB02", "fl")
	s = strings.ReplaceAll(s, "\uFB03", "ffi")
	s = strings.ReplaceAll(s, "\uFB04", "ffl")
	s = strings.ReplaceAll(s, "\uFB05", "st")
	s = strings.ReplaceAll(s, "\uFB06", "st")
	return sanitizeXMLChars(s)
}

// TextCell represents a cell within a line that has multi-column gaps.
type TextCell struct {
	Left      float64
	Right     float64
	PlainText string
	RichXHTML string
}

// VisualLine represents a single reconstructed visual horizontal line of text on a page.
type VisualLine struct {
	Top                float64
	Left               float64
	Width              float64
	Height             float64
	Right              float64
	FontSize           float64
	IsBold             bool
	IsItalic           bool
	IsMonospace        bool
	PlainText          string
	RichXHTML          string
	HasMultipleColumns bool
	CellStops          []TextCell
	PageNum            int
	PageWidth          float64
	PageHeight         float64
}

type textRunInfo struct {
	Top       float64
	Left      float64
	Width     float64
	Height    float64
	Right     float64
	Font      FontInfo
	PlainText string
	RichXHTML string
	IsBold    bool
	IsItalic  bool
}

// parseInnerXML extracts plain text and safe rich XHTML from pdftohtml <text> InnerXML.
func parseInnerXML(innerXML string, font FontInfo) (plainText string, richXHTML string, isBold bool, isItalic bool) {
	innerXML = expandUnicodeLigatures(innerXML)
	if innerXML == "" {
		return "", "", font.IsBold, font.IsItalic
	}

	contextNode := &html.Node{
		Type:     html.ElementNode,
		DataAtom: atom.Body,
		Data:     "body",
	}
	nodes, err := html.ParseFragment(strings.NewReader(innerXML), contextNode)
	if err != nil {
		// Fallback to plain text with HTML tags stripped
		clean := sanitizeXMLChars(stripHTMLTags(innerXML))
		var buf bytes.Buffer
		escapeXMLText(&buf, clean)
		return clean, buf.String(), font.IsBold, font.IsItalic
	}

	var plainBuf strings.Builder
	var richBuf bytes.Buffer
	hasBoldTag := false
	hasItalicTag := false

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			text := expandUnicodeLigatures(n.Data)
			plainBuf.WriteString(text)
			escapeXMLText(&richBuf, text)

		case html.ElementNode:
			tag := strings.ToLower(n.Data)
			switch tag {
			case "b", "strong":
				hasBoldTag = true
				richBuf.WriteString("<strong>")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				richBuf.WriteString("</strong>")
			case "i", "em":
				hasItalicTag = true
				richBuf.WriteString("<em>")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				richBuf.WriteString("</em>")
			case "sub":
				richBuf.WriteString("<sub>")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				richBuf.WriteString("</sub>")
			case "sup":
				richBuf.WriteString("<sup>")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				richBuf.WriteString("</sup>")
			case "code", "tt":
				richBuf.WriteString("<code>")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				richBuf.WriteString("</code>")
			case "s", "strike", "del":
				richBuf.WriteString("<del>")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				richBuf.WriteString("</del>")
			case "u":
				richBuf.WriteString("<u>")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				richBuf.WriteString("</u>")
			case "a":
				href := getAttr(n, "href")
				if href != "" {
					richBuf.WriteString(`<a href="`)
					escapeXMLAttr(&richBuf, href)
					richBuf.WriteString(`">`)
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				if href != "" {
					richBuf.WriteString("</a>")
				}
			default:
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
			}
		case html.DocumentNode:
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
	}

	for _, n := range nodes {
		walk(n)
	}

	plain := plainBuf.String()
	rich := richBuf.String()
	return plain, rich, font.IsBold || hasBoldTag, font.IsItalic || hasItalicTag
}

// BuildVisualLines groups horizontal <text> elements that belong to the same visual baseline.
func BuildVisualLines(page *xmlPage, fontMap map[string]FontInfo) []VisualLine {
	if page == nil || len(page.Texts) == 0 {
		return nil
	}

	// 1. Convert xmlTexts into parsed textRunInfo
	runs := make([]textRunInfo, 0, len(page.Texts))
	for _, t := range page.Texts {
		font, ok := fontMap[t.FontID]
		if !ok {
			font = FontInfo{
				ID:     t.FontID,
				Family: "sans-serif",
				Size:   t.Height,
			}
		}
		plain, rich, isBold, isItalic := parseInnerXML(t.InnerXML, font)
		if strings.TrimSpace(plain) == "" {
			continue
		}
		runs = append(runs, textRunInfo{
			Top:       t.Top,
			Left:      t.Left,
			Width:     t.Width,
			Height:    t.Height,
			Right:     t.Left + t.Width,
			Font:      font,
			PlainText: plain,
			RichXHTML: rich,
			IsBold:    isBold,
			IsItalic:  isItalic,
		})
	}

	if len(runs) == 0 {
		return nil
	}

	// 2. Sort runs by Top asc, then Left asc
	sort.Slice(runs, func(i, j int) bool {
		if math.Abs(runs[i].Top-runs[j].Top) > 3.0 {
			return runs[i].Top < runs[j].Top
		}
		return runs[i].Left < runs[j].Left
	})

	// 3. Cluster runs on overlapping baselines
	var clusters [][]textRunInfo
	for _, run := range runs {
		placed := false
		for idx := len(clusters) - 1; idx >= 0; idx-- {
			cluster := clusters[idx]
			if len(cluster) == 0 {
				continue
			}
			last := cluster[len(cluster)-1]

			// Baseline and Top overlap heuristics
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
		}
		if !placed {
			clusters = append(clusters, []textRunInfo{run})
		}
	}

	// 4. Build VisualLine for each cluster
	visualLines := make([]VisualLine, 0, len(clusters))
	for _, cluster := range clusters {
		if len(cluster) == 0 {
			continue
		}
		// Sort runs by Left ascending
		sort.Slice(cluster, func(i, j int) bool {
			return cluster[i].Left < cluster[j].Left
		})

		// Find dominant font and overall bounds
		fontWeights := make(map[string]int)
		var maxLenFont FontInfo
		maxLen := -1
		minTop := cluster[0].Top
		maxBottom := cluster[0].Top + cluster[0].Height
		minLeft := cluster[0].Left
		maxRight := cluster[0].Right

		for _, r := range cluster {
			fontWeights[r.Font.ID] += len(r.PlainText)
			if len(r.PlainText) > maxLen {
				maxLen = len(r.PlainText)
				maxLenFont = r.Font
			}
			if r.Top < minTop {
				minTop = r.Top
			}
			if r.Top+r.Height > maxBottom {
				maxBottom = r.Top + r.Height
			}
			if r.Left < minLeft {
				minLeft = r.Left
			}
			if r.Right > maxRight {
				maxRight = r.Right
			}
		}

		lineHeight := maxBottom - minTop
		dominantFont := maxLenFont
		lineFontSize := dominantFont.Size
		if lineFontSize <= 0 {
			lineFontSize = lineHeight
		}

		// Assemble plain text, rich XHTML, and detect multi-column gaps
		var plainBuilder strings.Builder
		var richBuilder strings.Builder

		var cells []TextCell
		var currentCellPlain strings.Builder
		var currentCellRich strings.Builder
		cellStartLeft := cluster[0].Left
		cellEndRight := cluster[0].Right

		hasMultiColumns := false

		for i, curr := range cluster {
			runRich := curr.RichXHTML
			// Check if run is monospace while line is proportional
			if curr.Font.IsMonospace && !dominantFont.IsMonospace {
				runRich = "<code>" + curr.RichXHTML + "</code>"
			}
			// Check if run is italic while line dominant font is not italic
			if curr.IsItalic && !dominantFont.IsItalic && !strings.Contains(runRich, "<em") && !strings.Contains(runRich, "<i") {
				runRich = "<em>" + curr.RichXHTML + "</em>"
			}
			// Check if run is bold while line dominant font is not bold
			if curr.IsBold && !dominantFont.IsBold && !strings.Contains(runRich, "<strong") && !strings.Contains(runRich, "<b") {
				runRich = "<strong>" + curr.RichXHTML + "</strong>"
			}

			if i == 0 {
				plainBuilder.WriteString(curr.PlainText)
				richBuilder.WriteString(runRich)
				currentCellPlain.WriteString(curr.PlainText)
				currentCellRich.WriteString(runRich)
				cellStartLeft = curr.Left
				cellEndRight = curr.Right
				continue
			}

			prev := cluster[i-1]
			gap := curr.Left - prev.Right
			charW := math.Max(5.0, curr.Height*0.52)

			// Normal inline continuation vs wide gap
			if gap > 2.2*charW {
				// Wide gap: finalize previous cell stop
				cells = append(cells, TextCell{
					Left:      cellStartLeft,
					Right:     cellEndRight,
					PlainText: strings.TrimSpace(currentCellPlain.String()),
					RichXHTML: strings.TrimSpace(currentCellRich.String()),
				})
				currentCellPlain.Reset()
				currentCellRich.Reset()
				cellStartLeft = curr.Left
				cellEndRight = curr.Right
				hasMultiColumns = true

				plainBuilder.WriteString("   ")
				richBuilder.WriteString("   ")
			} else {
				// Inline continuation
				cellEndRight = curr.Right
				if gap > 0.22*charW {
					prevPlain := plainBuilder.String()
					if len(prevPlain) > 0 {
						lastRune := rune(prevPlain[len(prevPlain)-1])
						firstRune := rune(curr.PlainText[0])
						if lastRune != ' ' && firstRune != ' ' &&
							lastRune != '(' && lastRune != '[' && lastRune != '"' && lastRune != '\'' &&
							firstRune != ')' && firstRune != ']' && firstRune != '"' && firstRune != '\'' &&
							firstRune != ',' && firstRune != '.' && firstRune != ':' && firstRune != ';' {
							plainBuilder.WriteByte(' ')
							richBuilder.WriteByte(' ')
							currentCellPlain.WriteByte(' ')
							currentCellRich.WriteByte(' ')
						}
					}
				}
			}

			plainBuilder.WriteString(curr.PlainText)
			richBuilder.WriteString(runRich)
			currentCellPlain.WriteString(curr.PlainText)
			currentCellRich.WriteString(runRich)
		}

		// Final cell
		if currentCellPlain.Len() > 0 {
			cells = append(cells, TextCell{
				Left:      cellStartLeft,
				Right:     cellEndRight,
				PlainText: strings.TrimSpace(currentCellPlain.String()),
				RichXHTML: strings.TrimSpace(currentCellRich.String()),
			})
		}

		plainText := strings.TrimSpace(plainBuilder.String())
		richXHTML := strings.TrimSpace(richBuilder.String())
		if plainText == "" {
			continue
		}

		visualLines = append(visualLines, VisualLine{
			Top:                minTop,
			Left:               minLeft,
			Width:              maxRight - minLeft,
			Height:             lineHeight,
			Right:              maxRight,
			FontSize:           lineFontSize,
			IsBold:             dominantFont.IsBold,
			IsItalic:           dominantFont.IsItalic,
			IsMonospace:        dominantFont.IsMonospace,
			PlainText:          plainText,
			RichXHTML:          richXHTML,
			HasMultipleColumns: hasMultiColumns && len(cells) >= 2,
			CellStops:          cells,
			PageNum:            page.Number,
			PageWidth:          page.Width,
			PageHeight:         page.Height,
		})
	}

	// Sort visualLines by Top asc, then Left asc
	sort.Slice(visualLines, func(i, j int) bool {
		if math.Abs(visualLines[i].Top-visualLines[j].Top) > 3.0 {
			return visualLines[i].Top < visualLines[j].Top
		}
		return visualLines[i].Left < visualLines[j].Left
	})

	return visualLines
}

var (
	pageNumberRegex      = regexp.MustCompile(`^(?i)(?:page\s+)?(\d+|[ivxlcdm]+)(?:\s*(?:/|of)\s*\d+)?$`)
	browserArtifactRegex = regexp.MustCompile(`^(?i)(about:blank|\d{1,2}/\d{1,2}/\d{2,4},?\s+\d{1,2}:\d{2})$`)
	digitRegex           = regexp.MustCompile(`\d+`)
)

func normalizeHeaderFooterTemplate(s string) string {
	s = digitRegex.ReplaceAllString(s, "#")
	return strings.ToLower(strings.TrimSpace(s))
}

// StripRunningNoise removes running headers, footers, page numbers, and print artifacts.
func StripRunningNoise(pages [][]VisualLine, pageHeights []float64) [][]VisualLine {
	if len(pages) == 0 {
		return pages
	}

	// 1. Gather cross-page header and footer template frequencies
	type templateLocation struct {
		Template string
		Zone     string // "header" or "footer"
		Y        float64
	}

	templateCount := make(map[string]int)

	for pIdx, lines := range pages {
		pHeight := 1000.0
		if pIdx < len(pageHeights) && pageHeights[pIdx] > 0 {
			pHeight = pageHeights[pIdx]
		}
		headerThreshold := 0.085 * pHeight
		footerThreshold := 0.915 * pHeight

		seenOnThisPage := make(map[string]bool)
		for _, line := range lines {
			isHeader := line.Top < headerThreshold
			isFooter := (line.Top + line.Height) > footerThreshold
			if !isHeader && !isFooter {
				continue
			}

			norm := normalizeHeaderFooterTemplate(line.PlainText)
			if norm == "" {
				continue
			}
			zone := "header"
			if isFooter {
				zone = "footer"
			}
			// Round Y position to nearest 10px to account for slight pagination jitter
			approxY := int(math.Round(line.Top/10.0)) * 10
			key := zone + ":" + strconv.Itoa(approxY) + ":" + norm
			if !seenOnThisPage[key] {
				seenOnThisPage[key] = true
				templateCount[key]++
			}
		}
	}

	totalNumPages := len(pages)
	minOccurrences := 3
	if totalNumPages >= 3 && totalNumPages <= 6 {
		minOccurrences = int(math.Ceil(float64(totalNumPages) * 0.40))
	}

	// 2. Filter lines per page
	result := make([][]VisualLine, len(pages))
	for pIdx, lines := range pages {
		pHeight := 1000.0
		if pIdx < len(pageHeights) && pageHeights[pIdx] > 0 {
			pHeight = pageHeights[pIdx]
		}
		headerThreshold := 0.085 * pHeight
		footerThreshold := 0.915 * pHeight

		filtered := make([]VisualLine, 0, len(lines))
		for _, line := range lines {
			isHeader := line.Top < headerThreshold
			isFooter := (line.Top + line.Height) > footerThreshold

			if isHeader || isFooter {
				trimmed := strings.TrimSpace(line.PlainText)
				// Check standalone page numbers
				if pageNumberRegex.MatchString(trimmed) {
					continue
				}
				// Check browser artifacts
				if browserArtifactRegex.MatchString(trimmed) {
					continue
				}
				// Check template frequency across pages
				norm := normalizeHeaderFooterTemplate(trimmed)
				zone := "header"
				if isFooter {
					zone = "footer"
				}
				approxY := int(math.Round(line.Top/10.0)) * 10
				key := zone + ":" + strconv.Itoa(approxY) + ":" + norm
				if templateCount[key] >= minOccurrences {
					continue
				}
			}

			filtered = append(filtered, line)
		}
		result[pIdx] = filtered
	}

	return result
}

// SplitPageColumns detects 2-column layouts and orders reading flow (Left column -> Right column).
func SplitPageColumns(lines []VisualLine, pageWidth float64) []VisualLine {
	if len(lines) < 6 || pageWidth <= 0 {
		return lines
	}

	corridorStart := 0.42 * pageWidth
	corridorEnd := 0.58 * pageWidth

	// Check if page has lines that cleanly divide around a center gutter
	leftCount := 0
	rightCount := 0
	crossingCount := 0
	var minBodyTop, maxBodyBottom float64
	initialized := false

	for _, l := range lines {
		// Ignore short lines or very wide spanning headers/titles
		if l.Width > 0.75*pageWidth {
			continue
		}
		if !initialized {
			minBodyTop = l.Top
			maxBodyBottom = l.Top + l.Height
			initialized = true
		} else {
			if l.Top < minBodyTop {
				minBodyTop = l.Top
			}
			if l.Top+l.Height > maxBodyBottom {
				maxBodyBottom = l.Top + l.Height
			}
		}

		if l.Right <= corridorEnd {
			leftCount++
		} else if l.Left >= corridorStart {
			rightCount++
		} else {
			crossingCount++
		}
	}

	verticalSpan := maxBodyBottom - minBodyTop
	// If both columns have significant content and few lines cross the gutter corridor
	if leftCount >= 4 && rightCount >= 4 && crossingCount <= 2 && verticalSpan >= 200.0 {
		var spanningTop []VisualLine
		var leftCol []VisualLine
		var rightCol []VisualLine
		var spanningBottom []VisualLine

		midX := pageWidth * 0.5

		// Spanning threshold: width > 65% of page width
		spanningThreshold := 0.65 * pageWidth

		for _, l := range lines {
			if l.Width >= spanningThreshold {
				if len(leftCol) == 0 && len(rightCol) == 0 {
					spanningTop = append(spanningTop, l)
				} else {
					spanningBottom = append(spanningBottom, l)
				}
			} else if l.Right <= midX+15.0 {
				leftCol = append(leftCol, l)
			} else {
				rightCol = append(rightCol, l)
			}
		}

		out := make([]VisualLine, 0, len(lines))
		out = append(out, spanningTop...)
		out = append(out, leftCol...)
		out = append(out, rightCol...)
		out = append(out, spanningBottom...)
		return out
	}

	return lines
}
