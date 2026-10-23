package converter

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	epub "github.com/go-shiori/go-epub"
	"github.com/vincent-petithory/dataurl"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type chapterData struct {
	Title    string
	Filename string
	Blocks   []ParsedBlock
	XHTML    string
}

func convertPDF(ctx context.Context, pdfPath, sourceLabel string, opts ConvertOptions, onProgress func(ProgressEvent)) (*Result, error) {
	start := time.Now()
	notify := func(step Step, msg string) {
		if onProgress != nil {
			onProgress(ProgressEvent{Step: step, Message: msg})
		}
	}

	// 0. Verify Poppler binaries
	pdfinfoBin, err := findPopplerBin(binPdfInfo)
	if err != nil {
		return nil, err
	}
	pdftohtmlBin, err := findPopplerBin(binPdfToHtml)
	if err != nil {
		return nil, err
	}
	pdftoppmBin, err := findPopplerBin(binPdfToPpm)
	if err != nil {
		return nil, err
	}

	// 1. Fetching & Poppler XML extraction
	notify(StepFetching, "Inspect PDF & extract spatial layout via Poppler...")
	info, err := runPDFInfo(ctx, pdfinfoBin, pdfPath)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect PDF: %w", err)
	}

	title := strings.TrimSpace(info.Title)
	if title == "" {
		base := filepath.Base(sourceLabel)
		ext := filepath.Ext(base)
		title = strings.TrimSpace(strings.TrimSuffix(base, ext))
		if title == "" {
			title = "Converted Document"
		}
	}
	author := strings.TrimSpace(info.Author)

	tmpDir, err := os.MkdirTemp("", "rebind-pdf-*")
	if err != nil {
		return nil, fmt.Errorf("create temp working dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	xmlPath, err := runPDFToHTMLXMLRange(ctx, pdftohtmlBin, pdfPath, tmpDir, opts.FirstPage, opts.LastPage)
	if err != nil {
		return nil, err
	}

	doc, err := parsePopplerXML(xmlPath)
	if err != nil {
		return nil, err
	}

	if info.Pages == 0 {
		info.Pages = len(doc.Pages)
	}

	// 2. Extracting: Spatial Layout & Noise Stripping
	notify(StepExtracting, "Remove headers/footers & reconstruct reading flow...")

	fontMap := make(map[string]FontInfo)
	for _, p := range doc.Pages {
		for _, f := range p.Fonts {
			if _, exists := fontMap[f.ID]; !exists {
				fontMap[f.ID] = classifyFont(f)
			}
		}
	}

	var pagesLines [][]VisualLine
	var pageHeights []float64

	for i := range doc.Pages {
		p := &doc.Pages[i]
		pageHeights = append(pageHeights, p.Height)
		lines := BuildVisualLines(p, fontMap)
		lines = SplitPageColumns(lines, p.Width)
		pagesLines = append(pagesLines, lines)
	}

	pagesLines = StripRunningNoise(pagesLines, pageHeights)

	profileUsed := opts.Profile
	if profileUsed == "" || profileUsed == ProfileAuto {
		profileUsed = ResolveBookProfile(doc, pagesLines)
	}

	lex := BuildDocumentLexicon(pagesLines)

	// 3. Localizing Assets: Render Figures, Tables, Code & States
	notify(StepLocalizingAssets, "Render figures, code blocks, tables & validate XHTML...")

	book, err := epub.NewEpub(title)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize EPUB: %w", err)
	}
	if author != "" {
		book.SetAuthor(author)
	}
	if info.Subject != "" {
		book.SetDescription(info.Subject)
	}

	var imgMu sync.Mutex
	imagesTotal := 0
	imagesSaved := 0
	embeddedCache := make(map[string]string)

	addImage := func(src string) (string, error) {
		imgMu.Lock()
		defer imgMu.Unlock()

		if p, ok := embeddedCache[src]; ok {
			return p, nil
		}

		localFile, err := resolvePopplerImagePath(tmpDir, src)
		if err != nil {
			return "", err
		}

		imagesTotal++
		ext := filepath.Ext(localFile)
		if ext == "" {
			ext = ".png"
		}
		internalName := fmt.Sprintf("img_%04d%s", imagesSaved+1, ext)
		internalPath, err := book.AddImage(localFile, internalName)
		if err != nil {
			return "", err
		}
		imagesSaved++
		embeddedCache[src] = internalPath
		return internalPath, nil
	}

	cropRegion := func(pageNum int, left, top, width, height float64, label string) (string, error) {
		imgMu.Lock()
		defer imgMu.Unlock()

		outPrefix := filepath.Join(tmpDir, fmt.Sprintf("crop_%s", label))
		pngFile, err := CropPageRegionPNG(ctx, pdftoppmBin, pdfPath, pageNum, left, top, width, height, outPrefix)
		if err != nil {
			return "", err
		}

		imagesTotal++
		internalName := fmt.Sprintf("crop_%04d.png", imagesSaved+1)
		internalPath, err := book.AddImage(pngFile, internalName)
		if err != nil {
			return "", err
		}
		imagesSaved++
		return internalPath, nil
	}

	// Detect cover image on the first min(3, len(doc.Pages)) pages
	// Page 1 is narrow spine/flap (width=216), Page 2 is true front cover (width=681)
	coverFound := false
	maxScan := min(3, len(doc.Pages))
	for pIdx := 0; pIdx < maxScan && !coverFound; pIdx++ {
		p := doc.Pages[pIdx]
		if p.Width < 300 {
			continue // skip narrow spine/flap
		}
		for _, img := range p.Images {
			if img.Width >= 0.65*p.Width && img.Height >= 0.65*p.Height {
				localCover, err := resolvePopplerImagePath(tmpDir, img.Src)
				if err == nil {
					coverInternal, err := book.AddImage(localCover, "cover"+filepath.Ext(localCover))
					if err == nil {
						_ = book.SetCover(coverInternal, "")
						imagesSaved++
						imagesTotal++
						coverFound = true
						break
					}
				}
			}
		}
	}

	stateConfig := StateMachineConfig{
		Profile:    profileUsed,
		CropRegion: cropRegion,
		AddImage:   addImage,
		Lexicon:    lex,
	}

	blocks := ParsePDFPages(doc, pagesLines, stateConfig)

	// 4. Packaging: Split into Chapters & Strict XHTML Validation
	notify(StepPackaging, "Package multi-chapter EPUB with reader stylesheet...")

	chapters := splitBlocksIntoChapters(blocks, doc.Outline, title, lex)

	cssDataURL := dataurl.EncodeBytes([]byte(epubStylesheet))
	cssPath, err := book.AddCSS(cssDataURL, "style.css")
	if err != nil {
		return nil, fmt.Errorf("failed to attach stylesheet: %w", err)
	}

	for _, ch := range chapters {
		validatedXHTML := ch.XHTML
		if err := validateXHTMLFragment(validatedXHTML); err != nil {
			// Safety net: pass through SanitizeToXHTML to ensure 100% XML validity
			clean, sErr := SanitizeToXHTML(validatedXHTML, nil, nil)
			if sErr == nil && clean != "" {
				validatedXHTML = clean
			}
		}

		if _, err := book.AddSection(validatedXHTML, ch.Title, ch.Filename, cssPath); err != nil {
			return nil, fmt.Errorf("failed to add chapter section %s: %w", ch.Filename, err)
		}
	}

	outputPath := opts.OutputPath
	if outputPath != "" {
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return nil, fmt.Errorf("failed to create output directory: %w", err)
		}
	} else {
		filename := SanitizeFilename(title) + ".epub"
		outputPath = uniqueOutputPath(opts.OutputDir, filename)
	}

	if err := book.Write(outputPath); err != nil {
		return nil, fmt.Errorf("failed to write EPUB: %w", err)
	}

	if err := PostProcessEPUB(outputPath); err != nil {
		return nil, fmt.Errorf("failed to post-process EPUB TOC: %w", err)
	}

	notify(StepComplete, fmt.Sprintf("Saved to %s", outputPath))

	return &Result{
		Title:       title,
		Author:      author,
		SourceURL:   sourceLabel,
		OutputPath:  outputPath,
		ImagesTotal: imagesTotal,
		ImagesSaved: imagesSaved,
		PagesTotal:  info.Pages,
		Chapters:    len(chapters),
		ProfileUsed: profileUsed,
		Duration:    time.Since(start),
	}, nil
}

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

var htmlTagStripRegex = regexp.MustCompile(`<[^>]+>`)

func firstLetterRuneInText(s string) rune {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return r
		}
	}
	return 0
}

func getLastPageNum(b ParsedBlock) int {
	matches := spanPageRegex.FindAllStringSubmatch(b.XHTML, -1)
	if len(matches) > 0 {
		last := matches[len(matches)-1]
		if p, err := strconv.Atoi(last[1]); err == nil {
			return p
		}
	}
	return b.PageNum
}

func mergeCrossPageParagraphs(blocks []ParsedBlock, lex *DocumentLexicon) []ParsedBlock {
	if len(blocks) < 2 {
		return blocks
	}
	var res []ParsedBlock

	for i := 0; i < len(blocks); i++ {
		curr := blocks[i]
		if len(res) == 0 {
			res = append(res, curr)
			continue
		}

		prev := &res[len(res)-1]
		if prev.Kind == BlockParagraph && curr.Kind == BlockParagraph && curr.PageNum == getLastPageNum(*prev)+1 {
			// Do not merge structured TOC lines or links across pages
			if strings.Contains(prev.XHTML, "toc-chapter-num") || strings.Contains(curr.XHTML, "toc-chapter-num") ||
				strings.Contains(prev.XHTML, "<a ") || strings.Contains(curr.XHTML, "<a ") {
				res = append(res, curr)
				continue
			}

			prevPlain := stripHTMLTags(prev.XHTML)
			currPlain := stripHTMLTags(curr.XHTML)

			prevEndsSentence := endsWithSentencePunct(prevPlain)
			currFirstLetter := firstLetterRuneInText(currPlain)
			currStartsLower := currFirstLetter != 0 && unicode.IsLower(currFirstLetter)

			if !prevEndsSentence || currStartsLower {
				// Merge curr into prev
				prevInner := strings.TrimSpace(prev.XHTML)
				if strings.HasPrefix(prevInner, "<p>") && strings.HasSuffix(prevInner, "</p>") {
					prevInner = prevInner[3 : len(prevInner)-4]
				}
				currInner := strings.TrimSpace(curr.XHTML)
				if strings.HasPrefix(currInner, "<p>") && strings.HasSuffix(currInner, "</p>") {
					currInner = currInner[3 : len(currInner)-4]
				}

				sep := " "
				prevInnerTrimmed := strings.TrimRight(prevInner, " ")
				if strings.HasSuffix(prevInnerTrimmed, "-") && lex != nil {
					prevWords := strings.Fields(prevPlain)
					currWords := strings.Fields(currPlain)
					if len(prevWords) > 0 && len(currWords) > 0 {
						w1 := prevWords[len(prevWords)-1]
						w2 := currWords[0]
						resolved := lex.Dehyphenate(w1, w2)
						trimIdx := strings.LastIndex(prevInnerTrimmed, w1)
						if trimIdx != -1 {
							prevInner = prevInnerTrimmed[:trimIdx] + resolved
							currWordsOnly := strings.SplitN(currInner, w2, 2)
							if len(currWordsOnly) > 1 {
								currInner = currWordsOnly[1]
							}
							sep = ""
						}
					}
				}

				prev.XHTML = fmt.Sprintf("<p>%s%s<span id=\"p%d\"></span>%s</p>",
					prevInner, sep, curr.PageNum, strings.TrimLeft(currInner, " "))
				continue
			}
		}

		res = append(res, curr)
	}

	return res
}

func splitBlocksIntoChapters(blocks []ParsedBlock, outline *xmlOutline, docTitle string, lex *DocumentLexicon) []chapterData {
	if len(blocks) == 0 {
		return []chapterData{
			{
				Title:    docTitle,
				Filename: "chapter_001.xhtml",
				XHTML:    "<p>Empty document.</p>",
			},
		}
	}

	// Strategy A: Split by Outline bookmarks if present
	var bookmarks []Bookmark
	if outline != nil {
		bookmarks = outline.Flatten()
	}

	if len(bookmarks) >= 2 {
		var rawChapters []chapterData
		bookmarkIdx := 0
		currentTitle := bookmarks[0].Title
		var currentBlocks []ParsedBlock

		for _, b := range blocks {
			// Advance bookmarkIdx if we reached the next bookmark's page
			for bookmarkIdx+1 < len(bookmarks) && b.PageNum >= bookmarks[bookmarkIdx+1].Page {
				if len(currentBlocks) > 0 {
					rawChapters = append(rawChapters, chapterData{
						Title:  currentTitle,
						Blocks: currentBlocks,
					})
					currentBlocks = nil
				}
				bookmarkIdx++
				currentTitle = bookmarks[bookmarkIdx].Title
			}
			currentBlocks = append(currentBlocks, b)
		}

		if len(currentBlocks) > 0 {
			rawChapters = append(rawChapters, chapterData{
				Title:  currentTitle,
				Blocks: currentBlocks,
			})
		}
		if len(rawChapters) > 0 {
			return finalizeChapters(rawChapters, docTitle, lex)
		}
	}

	// Strategy B: Split by BlockHeading1
	hasH1 := false
	for _, b := range blocks {
		if b.Kind == BlockHeading1 {
			hasH1 = true
			break
		}
	}

	if hasH1 {
		var rawChapters []chapterData
		var currentBlocks []ParsedBlock
		currentTitle := docTitle

		for _, b := range blocks {
			if b.Kind == BlockHeading1 {
				if len(currentBlocks) > 0 {
					rawChapters = append(rawChapters, chapterData{
						Title:  currentTitle,
						Blocks: currentBlocks,
					})
					currentBlocks = nil
				}
				if b.Title != "" {
					currentTitle = b.Title
				} else {
					currentTitle = fmt.Sprintf("Chapter %d", len(rawChapters)+1)
				}
			}
			currentBlocks = append(currentBlocks, b)
		}

		if len(currentBlocks) > 0 {
			rawChapters = append(rawChapters, chapterData{
				Title:  currentTitle,
				Blocks: currentBlocks,
			})
		}
		if len(rawChapters) > 0 {
			return finalizeChapters(rawChapters, docTitle, lex)
		}
	}

	// Strategy C: Fallback - Group by page intervals (max 20 pages per section)
	var rawChapters []chapterData
	var currentBlocks []ParsedBlock
	startPage := 1
	currentTitle := docTitle

	for _, b := range blocks {
		if b.PageNum-startPage >= 20 {
			if len(currentBlocks) > 0 {
				rawChapters = append(rawChapters, chapterData{
					Title:  currentTitle,
					Blocks: currentBlocks,
				})
				currentBlocks = nil
			}
			startPage = b.PageNum
			currentTitle = fmt.Sprintf("Section %d", len(rawChapters)+1)
		}
		currentBlocks = append(currentBlocks, b)
	}

	if len(currentBlocks) > 0 {
		rawChapters = append(rawChapters, chapterData{
			Title:  currentTitle,
			Blocks: currentBlocks,
		})
	}

	return finalizeChapters(rawChapters, docTitle, lex)
}

var spanPageRegex = regexp.MustCompile(`<span id="p(\d+)"></span>`)

func finalizeChapters(rawChapters []chapterData, docTitle string, lexOpt ...*DocumentLexicon) []chapterData {
	var lex *DocumentLexicon
	if len(lexOpt) > 0 {
		lex = lexOpt[0]
	}
	if len(rawChapters) == 0 {
		return []chapterData{
			{
				Title:    docTitle,
				Filename: "chapter_001.xhtml",
				XHTML:    "<p>Empty document.</p>",
			},
		}
	}

	pageToChapter := make(map[int]string)
	for i := range rawChapters {
		rawChapters[i].Filename = fmt.Sprintf("chapter_%03d.xhtml", i+1)
		// Merge cross-page paragraphs within the chapter (skip TOC/Contents chapters)
		if !strings.EqualFold(rawChapters[i].Title, "Contents") && !strings.EqualFold(rawChapters[i].Title, "Table of Contents") {
			rawChapters[i].Blocks = mergeCrossPageParagraphs(rawChapters[i].Blocks, lex)
		}

		// Ensure strictly at most one <h1> per chapter file
		h1Count := 0
		for bIdx := range rawChapters[i].Blocks {
			b := &rawChapters[i].Blocks[bIdx]
			if b.Kind == BlockHeading1 {
				h1Count++
				if h1Count > 1 {
					b.Kind = BlockHeading2
					b.XHTML = strings.Replace(b.XHTML, "<h1>", "<h2>", 1)
					b.XHTML = strings.Replace(b.XHTML, "</h1>", "</h2>", 1)
				}
			}
		}

		for _, b := range rawChapters[i].Blocks {
			if b.PageNum > 0 && pageToChapter[b.PageNum] == "" {
				pageToChapter[b.PageNum] = rawChapters[i].Filename
			}
		}
	}

	for i := range rawChapters {
		var buf bytes.Buffer
		seenPages := make(map[int]bool)
		for _, b := range rawChapters[i].Blocks {
			// Find any pages embedded via <span id="pN"></span>
			sub := spanPageRegex.FindAllStringSubmatch(b.XHTML, -1)
			for _, m := range sub {
				if p, err := strconv.Atoi(m[1]); err == nil {
					seenPages[p] = true
				}
			}

			if b.PageNum > 0 && !seenPages[b.PageNum] {
				seenPages[b.PageNum] = true
				buf.WriteString(fmt.Sprintf("<div id=\"p%d\"></div>\n", b.PageNum))
			}
			buf.WriteString(b.XHTML)
			buf.WriteByte('\n')
		}
		raw := buf.String()
		if strings.TrimSpace(raw) == "" {
			raw = "<p></p>"
		}
		rawChapters[i].XHTML = rewritePopplerLinks(raw, rawChapters[i].Filename, pageToChapter)
	}

	return rawChapters
}

var popplerPageLinkRegex = regexp.MustCompile(`(?i)(?:[a-zA-Z0-9_\-\.]*\.html)?#(\d+)`)

func rewritePopplerLinks(rawXHTML string, currentSection string, pageToChapter map[int]string) string {
	contextNode := &html.Node{
		Type:     html.ElementNode,
		DataAtom: atom.Body,
		Data:     "body",
	}
	nodes, err := html.ParseFragment(strings.NewReader(rawXHTML), contextNode)
	if err != nil {
		return rawXHTML
	}

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "a") {
			href := getAttr(n, "href")
			if href != "" && !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://") && !strings.HasPrefix(href, "mailto:") {
				if match := popplerPageLinkRegex.FindStringSubmatch(href); len(match) > 1 {
					pageNum, err := strconv.Atoi(match[1])
					if err == nil {
						if targetSection, ok := pageToChapter[pageNum]; ok {
							if targetSection == currentSection {
								setAttr(n, "href", fmt.Sprintf("#p%d", pageNum))
							} else {
								setAttr(n, "href", fmt.Sprintf("%s#p%d", targetSection, pageNum))
							}
						} else if strings.Contains(strings.ToLower(href), ".html") {
							// Unresolvable page link pointing to phantom .html file
							removeAttr(n, "href")
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	var buf bytes.Buffer
	for _, n := range nodes {
		walk(n)
		writeXHTMLNode(&buf, n, nil, nil)
	}
	return buf.String()
}
