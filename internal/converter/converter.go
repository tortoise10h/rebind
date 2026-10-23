package converter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gabriel-vasile/mimetype"
	epub "github.com/go-shiori/go-epub"
	readability "github.com/go-shiori/go-readability"
	"github.com/vincent-petithory/dataurl"
)

// Step represents the current stage of the conversion pipeline.
type Step int

const (
	StepFetching Step = iota + 1
	StepExtracting
	StepLocalizingAssets
	StepPackaging
	StepComplete
)

// ProgressEvent is emitted as the conversion progresses through the 4-step pipeline.
type ProgressEvent struct {
	Step    Step
	Message string
}

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
	OutputDir  string
	OutputPath string      // Optional explicit output .epub path (overrides OutputDir + title)
	Profile    BookProfile // Default: ProfileAuto (only used for PDF inputs)
	FirstPage  int         // Optional 1-based start page for PDF partial conversion (0 = start)
	LastPage   int         // Optional 1-based end page for PDF partial conversion (0 = end)
}

// Result holds details about the generated EPUB file.
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

const epubStylesheet = `
body {
	font-family: "Iowan Old Style", Georgia, Palatino, "Times New Roman", serif;
	line-height: 1.68;
	padding: 0.5em 0.8em;
	margin: 0 auto;
	overflow-wrap: break-word;
}

.article-header {
	margin-bottom: 1.8em;
	padding-bottom: 1em;
	border-bottom: 1px solid rgba(128, 128, 128, 0.3);
}

.article-title {
	font-size: 1.75em;
	line-height: 1.25;
	margin: 0 0 0.4em 0;
	font-weight: bold;
}

.article-meta {
	font-size: 0.9em;
	opacity: 0.8;
	margin: 0.2em 0;
}

.article-meta a {
	text-decoration: none;
}

h1, h2, h3, h4, h5, h6 {
	line-height: 1.3;
	margin-top: 1.5em;
	margin-bottom: 0.5em;
}

p {
	margin: 0 0 1em 0;
}

img {
	max-width: 100%;
	height: auto;
	display: block;
	margin: 1.2em auto;
	page-break-inside: avoid;
}

figure {
	margin: 1.4em 0;
	text-align: center;
	page-break-inside: avoid;
}

figcaption {
	font-size: 0.85em;
	opacity: 0.75;
	margin-top: 0.4em;
	font-style: italic;
}

blockquote {
	margin: 1.2em 0;
	padding: 0.2em 0 0.2em 1em;
	border-left: 3px solid rgba(128, 128, 128, 0.5);
	font-style: italic;
}

pre, code {
	font-family: "SF Mono", Menlo, Monaco, Consolas, "Courier New", monospace;
	font-size: 0.88em;
}

code {
	padding: 0.12em 0.3em;
	border-radius: 3px;
	background-color: rgba(128, 128, 128, 0.12);
}

pre {
	padding: 0.85em 1em;
	margin: 1.2em 0;
	border-radius: 5px;
	border: 1px solid rgba(128, 128, 128, 0.25);
	background-color: rgba(128, 128, 128, 0.08);
	white-space: pre-wrap;
	word-wrap: break-word;
	overflow-x: auto;
}

pre code {
	padding: 0;
	background: transparent;
}

hr {
	border: none;
	border-top: 1px solid rgba(128, 128, 128, 0.3);
	margin: 2em 0;
}

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

.toc-chapter-num {
	margin-top: 1.2em;
	margin-bottom: 0.2em;
	letter-spacing: 0.1em;
	text-transform: uppercase;
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

table {
	width: 100%;
	border-collapse: collapse;
	margin: 1.2em 0;
	font-size: 0.92em;
}

th, td {
	border: 1px solid rgba(128, 128, 128, 0.3);
	padding: 0.45em 0.6em;
	text-align: left;
}
`

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"

// DefaultOutputDir returns ~/Documents, creating it if needed.
func DefaultOutputDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	docsDir := filepath.Join(home, "Documents")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		return "", fmt.Errorf("ensure Documents directory: %w", err)
	}
	return docsDir, nil
}

// DetectInputKind identifies whether the input is a Web URL, local PDF path, or remote PDF URL.
func DetectInputKind(raw string) (InputKind, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return InputWebURL, "", fmt.Errorf("please enter a URL or PDF file path")
	}

	// 1. Strip surrounding quotes (drag & drop often wraps paths in quotes)
	if (strings.HasPrefix(trimmed, "'") && strings.HasSuffix(trimmed, "'") && len(trimmed) >= 2) ||
		(strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2) {
		trimmed = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	}

	// 2. Check for local file / drag-and-drop
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		localPath := trimmed

		// Handle file:// scheme
		if strings.HasPrefix(localPath, "file://") {
			if u, err := url.Parse(localPath); err == nil {
				if unescaped, err := url.PathUnescape(u.Path); err == nil {
					localPath = unescaped
				} else {
					localPath = u.Path
				}
			}
		}

		// Unescape backslash-escaped spaces from macOS terminal drag-and-drop
		localPath = strings.ReplaceAll(localPath, `\ `, " ")

		// Expand ~/
		if localPath == "~" || strings.HasPrefix(localPath, "~/") {
			home, err := os.UserHomeDir()
			if err == nil {
				if localPath == "~" {
					localPath = home
				} else {
					localPath = filepath.Join(home, strings.TrimPrefix(localPath, "~/"))
				}
			}
		}

		absPath, err := filepath.Abs(localPath)
		if err != nil {
			absPath = localPath
		}

		// Check if file exists
		if fi, err := os.Stat(absPath); err == nil && !fi.IsDir() {
			if strings.HasSuffix(strings.ToLower(absPath), ".pdf") {
				return InputLocalPDF, absPath, nil
			}
			// Check %PDF- magic bytes
			if f, err := os.Open(absPath); err == nil {
				magic := make([]byte, 5)
				n, _ := f.Read(magic)
				_ = f.Close()
				if n >= 5 && string(magic[:5]) == "%PDF-" {
					return InputLocalPDF, absPath, nil
				}
			}
		}

		// If it clearly looks like a file path (starts with /, ./, ../, ~/, or ends with .pdf)
		if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "./") ||
			strings.HasPrefix(trimmed, "../") || strings.HasPrefix(trimmed, "~/") ||
			strings.HasSuffix(strings.ToLower(trimmed), ".pdf") {
			return InputLocalPDF, "", fmt.Errorf("PDF file not found: %s", absPath)
		}
	}

	// 3. Remote HTTP/HTTPS URL
	normalizedURL, parsedURL, err := NormalizeURL(trimmed)
	if err != nil {
		return InputWebURL, "", err
	}

	if strings.HasSuffix(strings.ToLower(parsedURL.Path), ".pdf") {
		return InputRemotePDF, normalizedURL, nil
	}

	return InputWebURL, normalizedURL, nil
}

// Convert preserves backward compatibility and delegates to ConvertWithOptions.
func Convert(ctx context.Context, rawURL string, outputDir string, onProgress func(ProgressEvent)) (*Result, error) {
	return ConvertWithOptions(ctx, rawURL, ConvertOptions{
		OutputDir: outputDir,
		Profile:   ProfileAuto,
	}, onProgress)
}

// ConvertWithOptions executes the Web or PDF to EPUB conversion pipeline with configured options.
func ConvertWithOptions(ctx context.Context, rawInput string, opts ConvertOptions, onProgress func(ProgressEvent)) (*Result, error) {
	var err error
	if opts.OutputDir == "" {
		opts.OutputDir, err = DefaultOutputDir()
		if err != nil {
			return nil, err
		}
	}
	if opts.Profile == "" {
		opts.Profile = ProfileAuto
	}

	kind, target, err := DetectInputKind(rawInput)
	if err != nil {
		return nil, err
	}

	switch kind {
	case InputLocalPDF:
		return convertPDF(ctx, target, target, opts, onProgress)

	case InputRemotePDF:
		notify := func(step Step, msg string) {
			if onProgress != nil {
				onProgress(ProgressEvent{Step: step, Message: msg})
			}
		}
		notify(StepFetching, "Downloading remote PDF...")
		tmpPDF, err := os.CreateTemp("", "rebind-remote-*.pdf")
		if err != nil {
			return nil, fmt.Errorf("create temp pdf file: %w", err)
		}
		defer os.Remove(tmpPDF.Name())

		pdfBytes, _, _, err := fetchURLData(ctx, target)
		if err != nil {
			_ = tmpPDF.Close()
			return nil, fmt.Errorf("download remote pdf: %w", err)
		}
		if _, err := tmpPDF.Write(pdfBytes); err != nil {
			_ = tmpPDF.Close()
			return nil, fmt.Errorf("write temp pdf: %w", err)
		}
		_ = tmpPDF.Close()

		return convertPDF(ctx, tmpPDF.Name(), target, opts, onProgress)

	case InputWebURL:
		start := time.Now()
		notify := func(step Step, msg string) {
			if onProgress != nil {
				onProgress(ProgressEvent{Step: step, Message: msg})
			}
		}

		notify(StepFetching, "Fetching web content...")
		data, finalURL, contentType, err := fetchURLData(ctx, target)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch URL: %w", err)
		}

		// Auto-route to PDF pipeline if the response is actually a PDF
		if strings.HasPrefix(strings.ToLower(contentType), "application/pdf") || bytes.HasPrefix(data, []byte("%PDF-")) {
			tmpPDF, err := os.CreateTemp("", "rebind-routed-*.pdf")
			if err != nil {
				return nil, fmt.Errorf("create temp pdf: %w", err)
			}
			defer os.Remove(tmpPDF.Name())
			if _, err := tmpPDF.Write(data); err != nil {
				_ = tmpPDF.Close()
				return nil, fmt.Errorf("write routed pdf: %w", err)
			}
			_ = tmpPDF.Close()
			return convertPDF(ctx, tmpPDF.Name(), finalURL.String(), opts, onProgress)
		}

		// Otherwise proceed with standard Readability Web pipeline
		parsedURL := finalURL

		// 2. Readability Extraction
		notify(StepExtracting, "Extracting readable content with Readability...")
		article, err := readability.FromReader(bytes.NewReader(data), parsedURL)
		if err != nil {
			return nil, fmt.Errorf("failed to extract article content: %w", err)
		}
		if strings.TrimSpace(article.Content) == "" {
			return nil, fmt.Errorf("no readable article content found on %s", target)
		}

		title := strings.TrimSpace(article.Title)
		if title == "" {
			title = parsedURL.Host
		}
		author := strings.TrimSpace(article.Byline)
		if author == "" && strings.TrimSpace(article.SiteName) != "" {
			author = strings.TrimSpace(article.SiteName)
		}

		// Initialize EPUB
		book, err := epub.NewEpub(title)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize EPUB: %w", err)
		}
		if author != "" {
			book.SetAuthor(author)
		}
		if article.Excerpt != "" {
			book.SetDescription(article.Excerpt)
		}
		if article.Language != "" {
			book.SetLang(article.Language)
		}

		// 3. Asset Localizing & Strict XHTML Sanitization
		tmpDir, err := os.MkdirTemp("", "rebind-assets-*")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp asset dir: %w", err)
		}
		defer os.RemoveAll(tmpDir)

		imageURLs := ExtractImageURLs(article.Content, parsedURL)
		notify(StepLocalizingAssets, fmt.Sprintf("Localizing %d images & sanitizing XHTML...", len(imageURLs)))

		downloaded := downloadImagesConcurrently(ctx, imageURLs, tmpDir)
		epubImageMap := make(map[string]string, len(downloaded))
		for idx, remoteURL := range imageURLs {
			localFile, ok := downloaded[remoteURL]
			if !ok {
				continue
			}
			ext := filepath.Ext(localFile)
			internalName := fmt.Sprintf("img_%04d%s", idx+1, ext)
			internalPath, err := book.AddImage(localFile, internalName)
			if err == nil {
				epubImageMap[remoteURL] = internalPath
			}
		}

		bodyXHTML, err := SanitizeToXHTML(article.Content, parsedURL, func(remoteURL string) (string, bool) {
			p, ok := epubImageMap[remoteURL]
			return p, ok
		})
		if err != nil {
			return nil, fmt.Errorf("failed to sanitize XHTML: %w", err)
		}

		fullChapterXHTML := buildArticleXHTML(title, author, article.SiteName, parsedURL.String(), bodyXHTML)

		// 4. EPUB Packaging & Styling
		notify(StepPackaging, "Packaging EPUB archive with reader stylesheet...")
		cssDataURL := dataurl.EncodeBytes([]byte(epubStylesheet))
		cssPath, err := book.AddCSS(cssDataURL, "style.css")
		if err != nil {
			return nil, fmt.Errorf("failed to attach EPUB stylesheet: %w", err)
		}

		if _, err := book.AddSection(fullChapterXHTML, title, "article.xhtml", cssPath); err != nil {
			return nil, fmt.Errorf("failed to add article section to EPUB: %w", err)
		}

		filename := SanitizeFilename(title) + ".epub"
		outputPath := uniqueOutputPath(opts.OutputDir, filename)

		if err := book.Write(outputPath); err != nil {
			return nil, fmt.Errorf("failed to write EPUB file: %w", err)
		}

		if err := PostProcessEPUB(outputPath); err != nil {
			return nil, fmt.Errorf("failed to post-process EPUB TOC: %w", err)
		}

		notify(StepComplete, fmt.Sprintf("Saved to %s", outputPath))

		return &Result{
			Title:       title,
			Author:      author,
			SiteName:    article.SiteName,
			Excerpt:     article.Excerpt,
			SourceURL:   parsedURL.String(),
			OutputPath:  outputPath,
			ImagesTotal: len(imageURLs),
			ImagesSaved: len(epubImageMap),
			Chapters:    1,
			Duration:    time.Since(start),
		}, nil
	}

	return nil, fmt.Errorf("unsupported input kind")
}

// NormalizeURL validates and normalizes user input URLs.
func NormalizeURL(raw string) (string, *url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil, fmt.Errorf("please enter a web page URL")
	}
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return "", nil, fmt.Errorf("invalid URL: %q", raw)
	}
	return parsed.String(), parsed, nil
}

func fetchURLData(ctx context.Context, targetURL string) ([]byte, *url.URL, string, error) {
	client := &http.Client{
		Timeout: 45 * time.Second,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,application/pdf;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, nil, "", fmt.Errorf("unexpected HTTP status %d %s", resp.StatusCode, resp.Status)
	}

	contentType := resp.Header.Get("Content-Type")

	data, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		return nil, nil, "", err
	}
	return data, resp.Request.URL, contentType, nil
}

func fetchHTML(ctx context.Context, targetURL string) ([]byte, *url.URL, error) {
	data, u, _, err := fetchURLData(ctx, targetURL)
	return data, u, err
}

func downloadImagesConcurrently(ctx context.Context, urls []string, destDir string) map[string]string {
	results := make(map[string]string)
	if len(urls) == 0 {
		return results
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	for i, imgURL := range urls {
		wg.Add(1)
		go func(index int, rawURL string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			localPath, err := downloadSingleImage(ctx, client, rawURL, destDir, index)
			if err != nil {
				return
			}
			mu.Lock()
			results[rawURL] = localPath
			mu.Unlock()
		}(i, imgURL)
	}

	wg.Wait()
	return results
}

func downloadSingleImage(ctx context.Context, client *http.Client, imgURL string, destDir string, index int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 15*1024*1024))
	if err != nil || len(data) == 0 {
		return "", fmt.Errorf("empty image")
	}

	mtype := mimetype.Detect(data)
	ext := supportedImageExt(mtype.String(), imgURL)
	if ext == "" {
		return "", fmt.Errorf("unsupported image mime: %s", mtype.String())
	}

	localPath := filepath.Join(destDir, fmt.Sprintf("asset_%04d%s", index+1, ext))
	if err := os.WriteFile(localPath, data, 0o644); err != nil {
		return "", err
	}
	return localPath, nil
}

func supportedImageExt(mimeStr string, rawURL string) string {
	switch {
	case strings.HasPrefix(mimeStr, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(mimeStr, "image/png"):
		return ".png"
	case strings.HasPrefix(mimeStr, "image/gif"):
		return ".gif"
	case strings.HasPrefix(mimeStr, "image/webp"):
		return ".webp"
	case strings.HasPrefix(mimeStr, "image/svg+xml"):
		return ".svg"
	}
	if u, err := url.Parse(rawURL); err == nil {
		ext := strings.ToLower(filepath.Ext(u.Path))
		switch ext {
		case ".jpg", ".jpeg":
			return ".jpg"
		case ".png", ".gif", ".webp", ".svg":
			return ext
		}
	}
	return ""
}

func buildArticleXHTML(title, author, siteName, sourceURL, bodyXHTML string) string {
	var buf bytes.Buffer
	buf.WriteString(`<div class="article-header">`)
	buf.WriteString(`<h1 class="article-title">`)
	escapeXMLText(&buf, sanitizeXMLChars(title))
	buf.WriteString(`</h1>`)

	var metaParts []string
	if author != "" {
		metaParts = append(metaParts, author)
	}
	if siteName != "" && !strings.EqualFold(siteName, author) {
		metaParts = append(metaParts, siteName)
	}
	if len(metaParts) > 0 {
		buf.WriteString(`<p class="article-meta">`)
		escapeXMLText(&buf, sanitizeXMLChars(strings.Join(metaParts, " · ")))
		buf.WriteString(`</p>`)
	}
	if sourceURL != "" {
		buf.WriteString(`<p class="article-meta"><a href="`)
		escapeXMLAttr(&buf, sanitizeXMLChars(sourceURL))
		buf.WriteString(`">`)
		escapeXMLText(&buf, sanitizeXMLChars(sourceURL))
		buf.WriteString(`</a></p>`)
	}
	buf.WriteString(`</div>`)
	buf.WriteString("\n")
	buf.WriteString(bodyXHTML)
	return buf.String()
}

var unsafeFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9\p{L}\p{N}\s\-_]+`)
var multipleSpaces = regexp.MustCompile(`\s+`)

// SanitizeFilename produces a clean, readable filename for the generated EPUB.
func SanitizeFilename(title string) string {
	cleaned := unsafeFilenameChars.ReplaceAllString(title, " ")
	cleaned = multipleSpaces.ReplaceAllString(strings.TrimSpace(cleaned), " ")
	if len(cleaned) > 80 {
		cleaned = strings.TrimSpace(cleaned[:80])
	}
	if cleaned == "" {
		return "article"
	}
	return cleaned
}

func uniqueOutputPath(dir, filename string) string {
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	candidate := filepath.Join(dir, filename)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	for i := 2; i < 1000; i++ {
		next := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(next); os.IsNotExist(err) {
			return next
		}
	}
	return candidate
}
