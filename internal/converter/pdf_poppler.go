package converter

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Poppler binary names.
const (
	binPdfInfo   = "pdfinfo"
	binPdfToHtml = "pdftohtml"
	binPdfToPpm  = "pdftoppm"
)

// Common fallbacks for macOS/Linux if PATH is minimal.
var fallbackBinDirs = []string{
	"/opt/homebrew/bin",
	"/usr/local/bin",
	"/usr/bin",
	"/bin",
}

// findPopplerBin resolves the absolute path to a Poppler utility.
func findPopplerBin(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	for _, dir := range fallbackBinDirs {
		candidate := filepath.Join(dir, name)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("Poppler tools (%s) not found; install with: brew install poppler", name)
}

type pdfInfo struct {
	Title   string
	Author  string
	Pages   int
	Subject string
}

var junkTitleRegex = regexp.MustCompile(`(?i)^(about:blank|untitled|none|document|\.pdf)$`)
var wordTitlePrefixRegex = regexp.MustCompile(`(?i)^microsoft\s+word\s+-\s+`)

// runPDFInfo extracts document metadata via `pdfinfo <pdfPath>`.
func runPDFInfo(ctx context.Context, pdfinfoBin, pdfPath string) (*pdfInfo, error) {
	cmd := exec.CommandContext(ctx, pdfinfoBin, pdfPath)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("pdfinfo execution failed: %w", err)
	}

	info := &pdfInfo{}
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		colonIdx := strings.IndexByte(line, ':')
		if colonIdx == -1 {
			continue
		}
		key := strings.TrimSpace(line[:colonIdx])
		val := strings.TrimSpace(line[colonIdx+1:])

		switch strings.ToLower(key) {
		case "title":
			val = wordTitlePrefixRegex.ReplaceAllString(val, "")
			val = strings.TrimSpace(val)
			if !junkTitleRegex.MatchString(val) {
				info.Title = val
			}
		case "author":
			info.Author = val
		case "subject":
			info.Subject = val
		case "pages":
			if p, err := strconv.Atoi(val); err == nil {
				info.Pages = p
			}
		}
	}

	return info, nil
}

// runPDFToHTMLXML runs `pdftohtml -xml -nodrm -enc UTF-8 <pdfPath> <tmpDir>/book.xml`
func runPDFToHTMLXML(ctx context.Context, pdftohtmlBin, pdfPath, tmpDir string) (string, error) {
	return runPDFToHTMLXMLRange(ctx, pdftohtmlBin, pdfPath, tmpDir, 0, 0)
}

// runPDFToHTMLXMLRange runs `pdftohtml -xml -nodrm -enc UTF-8 [-f N] [-l M] <pdfPath> <tmpDir>/book.xml`
func runPDFToHTMLXMLRange(ctx context.Context, pdftohtmlBin, pdfPath, tmpDir string, firstPage, lastPage int) (string, error) {
	if abs, err := filepath.Abs(pdfPath); err == nil {
		pdfPath = abs
	}
	outXML := filepath.Join(tmpDir, "book.xml")
	args := []string{"-xml", "-nodrm", "-enc", "UTF-8"}
	if firstPage > 0 {
		args = append(args, "-f", strconv.Itoa(firstPage))
	}
	if lastPage > 0 {
		args = append(args, "-l", strconv.Itoa(lastPage))
	}
	args = append(args, pdfPath, outXML)

	cmd := exec.CommandContext(ctx, pdftohtmlBin, args...)
	cmd.Dir = tmpDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pdftohtml failed: %w (stderr: %s)", err, stderr.String())
	}

	if _, err := os.Stat(outXML); err != nil {
		return "", fmt.Errorf("pdftohtml did not produce book.xml: %w", err)
	}

	return outXML, nil
}

// XML schema models for pdftohtml -xml output.

type xmlDocument struct {
	XMLName xml.Name    `xml:"pdf2xml"`
	Pages   []xmlPage   `xml:"page"`
	Outline *xmlOutline `xml:"outline"`
}

type xmlPage struct {
	Number int           `xml:"number,attr"`
	Top    float64       `xml:"top,attr"`
	Left   float64       `xml:"left,attr"`
	Width  float64       `xml:"width,attr"`
	Height float64       `xml:"height,attr"`
	Fonts  []xmlFontSpec `xml:"fontspec"`
	Images []xmlImage    `xml:"image"`
	Texts  []xmlText     `xml:"text"`
}

type xmlFontSpec struct {
	ID     string  `xml:"id,attr"`
	Size   float64 `xml:"size,attr"`
	Family string  `xml:"family,attr"`
	Color  string  `xml:"color,attr"`
}

type xmlImage struct {
	Top    float64 `xml:"top,attr"`
	Left   float64 `xml:"left,attr"`
	Width  float64 `xml:"width,attr"`
	Height float64 `xml:"height,attr"`
	Src    string  `xml:"src,attr"`
}

type xmlText struct {
	Top      float64 `xml:"top,attr"`
	Left     float64 `xml:"left,attr"`
	Width    float64 `xml:"width,attr"`
	Height   float64 `xml:"height,attr"`
	FontID   string  `xml:"font,attr"`
	InnerXML string  `xml:",innerxml"`
}

type xmlOutline struct {
	Items    []xmlOutlineItem `xml:"item"`
	Outlines []xmlOutline     `xml:"outline"`
}

type xmlOutlineItem struct {
	Page     int          `xml:"page,attr"`
	Title    string       `xml:",chardata"`
	Items    []xmlOutline `xml:"outline"`
}

var (
	tocPageSuffixRegex = regexp.MustCompile(`\s*[·•]\s*\d+\s*$`)
	romanNumeralRegex  = regexp.MustCompile(`(?i)^[ivxlcdm]+$`)
)

type Bookmark struct {
	Page  int
	Title string
}

func (o *xmlOutline) Flatten() []Bookmark {
	if o == nil {
		return nil
	}
	var res []Bookmark
	for _, it := range o.Items {
		t := strings.TrimSpace(it.Title)
		t = tocPageSuffixRegex.ReplaceAllString(t, "")
		if it.Page > 0 && t != "" {
			if romanNumeralRegex.MatchString(t) {
				continue
			}
			res = append(res, Bookmark{Page: it.Page, Title: t})
		}
	}
	sort.SliceStable(res, func(i, j int) bool {
		return res[i].Page < res[j].Page
	})
	return res
}

func parsePopplerXML(xmlPath string) (*xmlDocument, error) {
	data, err := os.ReadFile(xmlPath)
	if err != nil {
		return nil, fmt.Errorf("read poppler xml: %w", err)
	}

	var doc xmlDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal poppler xml: %w", err)
	}

	return &doc, nil
}

// FontInfo contains normalized typography information.
type FontInfo struct {
	ID          string
	Family      string
	Size        float64
	Color       string
	IsMonospace bool
	IsBold      bool
	IsItalic    bool
}

var fontSubsetPrefix = regexp.MustCompile(`^[A-Z]{6}\+`)

var monospaceFontNames = []string{
	"mono", "courier", "consolas", "menlo", "monaco", "firacode", "fira code", "fira-code",
	"sourcecode", "source code", "source-code", "inconsolata", "jetbrains", "dejavusansmono",
	"liberationsansmono", "lucidaconsole", "lucida console", "lucida-console",
	"lucideconsole", "typewriter", "fixed", "ubuntu mono", "sf mono", "roboto mono",
	"cascadia code", "cascadia-code",
}

var boldFontTokens = []string{
	"bold", "black", "heavy", "demi", "semibold",
}

var italicFontTokens = []string{
	"italic", "oblique", "slanted",
}

func classifyFont(spec xmlFontSpec) FontInfo {
	cleanedFamily := fontSubsetPrefix.ReplaceAllString(spec.Family, "")
	lower := strings.ToLower(cleanedFamily)

	isMono := false
	for _, m := range monospaceFontNames {
		if strings.Contains(lower, m) {
			isMono = true
			break
		}
	}

	isBold := false
	for _, b := range boldFontTokens {
		if strings.Contains(lower, b) {
			isBold = true
			break
		}
	}

	isItalic := false
	for _, it := range italicFontTokens {
		if strings.Contains(lower, it) {
			isItalic = true
			break
		}
	}

	return FontInfo{
		ID:          spec.ID,
		Family:      cleanedFamily,
		Size:        spec.Size,
		Color:       spec.Color,
		IsMonospace: isMono,
		IsBold:      isBold,
		IsItalic:    isItalic,
	}
}

// CropPageRegionPNG crops a specific region of a PDF page at 300 DPI using pdftoppm.
// Coordinates are in pdftohtml points (108 DPI).
func CropPageRegionPNG(ctx context.Context, pdftoppmBin, pdfPath string, pageNum int, left, top, width, height float64, outFilePrefix string) (string, error) {
	if abs, err := filepath.Abs(pdfPath); err == nil {
		pdfPath = abs
	}
	scale := 300.0 / 108.0
	pad := 4.0

	cropX := max(0, int(math.Floor((left-pad)*scale)))
	cropY := max(0, int(math.Floor((top-pad)*scale)))
	cropW := max(10, int(math.Ceil((width+2*pad)*scale)))
	cropH := max(10, int(math.Ceil((height+2*pad)*scale)))

	// pdftoppm -png -r 300 -f <pageNum> -l <pageNum> -singlefile -x <cropX> -y <cropY> -W <cropW> -H <cropH> <pdfPath> <outFilePrefix>
	cmd := exec.CommandContext(ctx, pdftoppmBin,
		"-png",
		"-r", "300",
		"-f", strconv.Itoa(pageNum),
		"-l", strconv.Itoa(pageNum),
		"-singlefile",
		"-x", strconv.Itoa(cropX),
		"-y", strconv.Itoa(cropY),
		"-W", strconv.Itoa(cropW),
		"-H", strconv.Itoa(cropH),
		pdfPath,
		outFilePrefix,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pdftoppm crop failed: %w (stderr: %s)", err, stderr.String())
	}

	targetPNG := outFilePrefix + ".png"
	if _, err := os.Stat(targetPNG); err != nil {
		return "", fmt.Errorf("cropped image %s not found: %w", targetPNG, err)
	}

	return targetPNG, nil
}
