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
	"strings"
	"sync"
	"time"

	"github.com/gabriel-vasile/mimetype"
	epub "github.com/go-shiori/go-epub"
	readability "github.com/go-shiori/go-readability"
	"github.com/vincent-petithory/dataurl"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"

// convertWeb executes the Web to EPUB conversion pipeline using Readability.
func convertWeb(ctx context.Context, target string, opts ConvertOptions, onProgress func(ProgressEvent)) (*Result, error) {
	start := time.Now()
	notify := func(step Step, msg string) {
		if onProgress != nil {
			onProgress(ProgressEvent{Step: step, Message: msg})
		}
	}

	// 1. Fetching
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

	outputPath := opts.OutputPath
	if outputPath == "" {
		filename := SanitizeFilename(title) + ".epub"
		outputPath = uniqueOutputPath(opts.OutputDir, filename)
	}

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
