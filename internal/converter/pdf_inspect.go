package converter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RawRunDump provides diagnostic visibility into a single <text> tag emitted by Poppler.
type RawRunDump struct {
	Top        float64 `json:"top"`
	Left       float64 `json:"left"`
	Width      float64 `json:"width"`
	Height     float64 `json:"height"`
	Bottom     float64 `json:"bottom"`
	FontSize   float64 `json:"font_size"`
	FontFamily string  `json:"font_family"`
	FontID     string  `json:"font_id"`
	Text       string  `json:"text"`
}

// RawImgDump provides diagnostic visibility into a single <image> tag emitted by Poppler.
type RawImgDump struct {
	Top    float64 `json:"top"`
	Left   float64 `json:"left"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Bottom float64 `json:"bottom"`
	Src    string  `json:"src"`
}

// PageInspection contains detailed pipeline intermediate state for one PDF page.
type PageInspection struct {
	PageNum      int           `json:"page_num"`
	Width        float64       `json:"width"`
	Height       float64       `json:"height"`
	RawTexts     []RawRunDump  `json:"raw_texts"`
	RawImages    []RawImgDump  `json:"raw_images"`
	RawLines     []VisualLine  `json:"raw_lines"`     // Before StripRunningNoise
	CleanLines   []VisualLine  `json:"clean_lines"`   // After StripRunningNoise
	NoiseRemoved []VisualLine  `json:"noise_removed"` // Lines stripped as headers/footers/page nums
	Blocks       []ParsedBlock `json:"blocks"`
}

// InspectionReport aggregates diagnostic information across requested pages of a PDF.
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

// InspectPDFPages extracts and exposes intermediate pipeline representations for inspection.
func InspectPDFPages(ctx context.Context, pdfPath string, firstPage, lastPage int, profile BookProfile) (*InspectionReport, error) {
	pdfinfoBin, err := findPopplerBin(binPdfInfo)
	if err != nil {
		return nil, err
	}
	pdftohtmlBin, err := findPopplerBin(binPdfToHtml)
	if err != nil {
		return nil, err
	}

	info, err := runPDFInfo(ctx, pdfinfoBin, pdfPath)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect PDF: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "rebind-inspect-*")
	if err != nil {
		return nil, fmt.Errorf("create temp inspect dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	xmlPath, err := runPDFToHTMLXMLRange(ctx, pdftohtmlBin, pdfPath, tmpDir, firstPage, lastPage)
	if err != nil {
		return nil, err
	}

	doc, err := parsePopplerXML(xmlPath)
	if err != nil {
		return nil, err
	}

	fontMap := make(map[string]FontInfo)
	for _, p := range doc.Pages {
		for _, f := range p.Fonts {
			if _, exists := fontMap[f.ID]; !exists {
				fontMap[f.ID] = classifyFont(f)
			}
		}
	}

	var rawPagesLines [][]VisualLine
	var pageHeights []float64

	for i := range doc.Pages {
		p := &doc.Pages[i]
		pageHeights = append(pageHeights, p.Height)
		lines := BuildVisualLines(p, fontMap)
		lines = SplitPageColumns(lines, p.Width)
		rawPagesLines = append(rawPagesLines, lines)
	}

	cleanPagesLines := StripRunningNoise(rawPagesLines, pageHeights)

	profileUsed := profile
	if profileUsed == "" || profileUsed == ProfileAuto {
		profileUsed = ResolveBookProfile(doc, cleanPagesLines)
	}

	lex := BuildDocumentLexicon(cleanPagesLines)

	// Calculate medians across clean lines
	var allFontSizes []float64
	var allPitches []float64
	var allHeights []float64

	for _, lines := range cleanPagesLines {
		for i, l := range lines {
			if l.FontSize > 0 {
				allFontSizes = append(allFontSizes, l.FontSize)
			}
			if l.Height > 0 {
				allHeights = append(allHeights, l.Height)
			}
			if i > 0 {
				pitch := l.Top - lines[i-1].Top
				if pitch > 0 && pitch < 100 {
					allPitches = append(allPitches, pitch)
				}
			}
		}
	}

	medianFontSize := median(allFontSizes, 12.0)
	medianPitch := median(allPitches, 14.0)
	medianHeight := median(allHeights, 12.0)

	stateConfig := StateMachineConfig{
		Profile:      profileUsed,
		MedianPitch:  medianPitch,
		MedianHeight: medianHeight,
		Lexicon:      lex,
		AddImage: func(src string) (string, error) {
			return filepath.Base(src), nil
		},
		CropRegion: func(pageNum int, left, top, width, height float64, label string) (string, error) {
			return fmt.Sprintf("crop_p%d_%s.png", pageNum, label), nil
		},
	}

	allBlocks := ParsePDFPages(doc, cleanPagesLines, stateConfig)

	// Group parsed blocks by PageNum
	blocksByPage := make(map[int][]ParsedBlock)
	for _, b := range allBlocks {
		blocksByPage[b.PageNum] = append(blocksByPage[b.PageNum], b)
	}

	var pageInspections []PageInspection
	for i, p := range doc.Pages {
		actualPageNum := p.Number
		if actualPageNum <= 0 {
			actualPageNum = i + 1
		}

		var rawTexts []RawRunDump
		for _, t := range p.Texts {
			f := fontMap[t.FontID]
			rawTexts = append(rawTexts, RawRunDump{
				Top:        t.Top,
				Left:       t.Left,
				Width:      t.Width,
				Height:     t.Height,
				Bottom:     t.Top + t.Height,
				FontSize:   f.Size,
				FontFamily: f.Family,
				FontID:     t.FontID,
				Text:       strings.TrimSpace(t.InnerXML),
			})
		}

		var rawImages []RawImgDump
		for _, img := range p.Images {
			rawImages = append(rawImages, RawImgDump{
				Top:    img.Top,
				Left:   img.Left,
				Width:  img.Width,
				Height: img.Height,
				Bottom: img.Top + img.Height,
				Src:    img.Src,
			})
		}

		rawLines := rawPagesLines[i]
		cleanLines := cleanPagesLines[i]

		// Identify stripped noise lines
		cleanSet := make(map[string]bool)
		for _, cl := range cleanLines {
			key := fmt.Sprintf("%.2f:%.2f:%s", cl.Top, cl.Left, cl.PlainText)
			cleanSet[key] = true
		}

		var noiseRemoved []VisualLine
		for _, rl := range rawLines {
			key := fmt.Sprintf("%.2f:%.2f:%s", rl.Top, rl.Left, rl.PlainText)
			if !cleanSet[key] {
				noiseRemoved = append(noiseRemoved, rl)
			}
		}

		pageInspections = append(pageInspections, PageInspection{
			PageNum:      actualPageNum,
			Width:        p.Width,
			Height:       p.Height,
			RawTexts:     rawTexts,
			RawImages:    rawImages,
			RawLines:     rawLines,
			CleanLines:   cleanLines,
			NoiseRemoved: noiseRemoved,
			Blocks:       blocksByPage[actualPageNum],
		})
	}

	var bookmarks []Bookmark
	if doc.Outline != nil {
		bookmarks = doc.Outline.Flatten()
	}

	title := strings.TrimSpace(info.Title)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(pdfPath), filepath.Ext(pdfPath))
	}

	return &InspectionReport{
		PDFPath:        pdfPath,
		Title:          title,
		Author:         strings.TrimSpace(info.Author),
		TotalPages:     info.Pages,
		ProfileUsed:    profileUsed,
		MedianFontSize: medianFontSize,
		MedianPitch:    medianPitch,
		MedianHeight:   medianHeight,
		Bookmarks:      bookmarks,
		Pages:          pageInspections,
	}, nil
}
