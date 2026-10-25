package converter

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
		return convertRemotePDF(ctx, target, opts, onProgress)

	case InputWebURL:
		return convertWeb(ctx, target, opts, onProgress)
	}

	return nil, fmt.Errorf("unsupported input kind")
}

func convertRemotePDF(ctx context.Context, target string, opts ConvertOptions, onProgress func(ProgressEvent)) (*Result, error) {
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
