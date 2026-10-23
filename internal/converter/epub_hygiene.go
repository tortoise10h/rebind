package converter

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	opfIDRegex      = regexp.MustCompile(`(?s)<dc:identifier[^>]*>(.*?)</dc:identifier>`)
	opfCreatorRegex = regexp.MustCompile(`(?s)<dc:creator[^>]*>(.*?)</dc:creator>`)
	navPointRegex   = regexp.MustCompile(`(?i)<navPoint\b([^>]*)>`)
)

// PostProcessEPUB inspects the written EPUB zip and fixes EPUB 2/3 TOC compatibility:
// 1. In toc.ncx: injects dtb:uid matching OPF unique-identifier, dtb:depth="1", dtb:totalPageCount="0",
//    dtb:maxPageNumber="0", docAuthor, and strictly compliant playOrder attributes on all navPoints.
// 2. In package.opf: adds <guide> with references to TOC and cover if missing.
// 3. In nav.xhtml: ensures id="toc" on <nav epub:type="toc"> and adds landmarks navigation.
func PostProcessEPUB(epubPath string) error {
	r, err := zip.OpenReader(epubPath)
	if err != nil {
		return fmt.Errorf("open epub zip %s: %w", epubPath, err)
	}
	defer r.Close()

	type zipEntry struct {
		name   string
		data   []byte
		method uint16
	}
	var entries []zipEntry

	var opfName string
	var opfData []byte
	var ncxName string
	var ncxData []byte
	var navName string
	var navData []byte

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return err
		}

		if strings.HasSuffix(f.Name, ".opf") {
			opfName = f.Name
			opfData = data
		} else if strings.HasSuffix(f.Name, "toc.ncx") {
			ncxName = f.Name
			ncxData = data
		} else if strings.HasSuffix(f.Name, "nav.xhtml") {
			navName = f.Name
			navData = data
		}

		entries = append(entries, zipEntry{
			name:   f.Name,
			data:   data,
			method: f.Method,
		})
	}

	if opfName == "" {
		return nil
	}

	// 1. Extract identifier and author from OPF
	opfStr := string(opfData)
	bookID := "urn:uuid:unknown"
	if m := opfIDRegex.FindStringSubmatch(opfStr); len(m) > 1 {
		bookID = strings.TrimSpace(m[1])
	}
	bookAuthor := ""
	if m := opfCreatorRegex.FindStringSubmatch(opfStr); len(m) > 1 {
		bookAuthor = strings.TrimSpace(m[1])
	}

	// 2. Fix toc.ncx
	if ncxName != "" && len(ncxData) > 0 {
		ncxStr := string(ncxData)

		// Replace <head> with standards-compliant DAISY/NCX metadata
		headRegex := regexp.MustCompile(`(?s)<head>.*?</head>`)
		newHead := fmt.Sprintf(`<head>
    <meta name="dtb:uid" content="%s"/>
    <meta name="dtb:depth" content="1"/>
    <meta name="dtb:totalPageCount" content="0"/>
    <meta name="dtb:maxPageNumber" content="0"/>
  </head>`, bookID)
		ncxStr = headRegex.ReplaceAllString(ncxStr, newHead)

		// Populate docAuthor if empty
		if bookAuthor != "" {
			emptyAuthorRegex := regexp.MustCompile(`(?s)<docAuthor>\s*<text></text>\s*</docAuthor>`)
			ncxStr = emptyAuthorRegex.ReplaceAllString(ncxStr, fmt.Sprintf("<docAuthor>\n    <text>%s</text>\n  </docAuthor>", bookAuthor))
		}

		// Add playOrder sequentially to every navPoint
		playOrder := 1
		ncxStr = navPointRegex.ReplaceAllStringFunc(ncxStr, func(match string) string {
			if strings.Contains(strings.ToLower(match), "playorder=") {
				return match
			}
			tag := strings.TrimRight(match, ">")
			res := fmt.Sprintf("%s playOrder=\"%d\">", tag, playOrder)
			playOrder++
			return res
		})

		for i := range entries {
			if entries[i].name == ncxName {
				entries[i].data = []byte(ncxStr)
				break
			}
		}
	}

	// 3. Ensure <guide> in package.opf
	if !strings.Contains(opfStr, "<guide>") && !strings.Contains(opfStr, "<guide ") {
		// Find TOC target: prefer chapter_002.xhtml (Contents) or nav.xhtml
		tocHref := ""
		for _, e := range entries {
			if strings.HasSuffix(e.name, "chapter_002.xhtml") {
				opfDir := path.Dir(opfName)
				rel, _ := filepath.Rel(opfDir, e.name)
				tocHref = filepath.ToSlash(rel)
				break
			}
		}
		if tocHref == "" && navName != "" {
			opfDir := path.Dir(opfName)
			rel, _ := filepath.Rel(opfDir, navName)
			tocHref = filepath.ToSlash(rel)
		}

		coverHref := ""
		for _, e := range entries {
			if strings.HasSuffix(e.name, "cover.xhtml") {
				opfDir := path.Dir(opfName)
				rel, _ := filepath.Rel(opfDir, e.name)
				coverHref = filepath.ToSlash(rel)
				break
			}
		}

		var guideBuf strings.Builder
		guideBuf.WriteString("  <guide>\n")
		if coverHref != "" {
			guideBuf.WriteString(fmt.Sprintf("    <reference type=\"cover\" title=\"Cover\" href=\"%s\"/>\n", coverHref))
		}
		if tocHref != "" {
			guideBuf.WriteString(fmt.Sprintf("    <reference type=\"toc\" title=\"Table of Contents\" href=\"%s\"/>\n", tocHref))
		}
		guideBuf.WriteString("  </guide>\n</package>")

		opfStr = strings.Replace(opfStr, "</package>", guideBuf.String(), 1)
		for i := range entries {
			if entries[i].name == opfName {
				entries[i].data = []byte(opfStr)
				break
			}
		}
	}

	// 4. Ensure nav.xhtml has id="toc" and landmarks
	if navName != "" && len(navData) > 0 {
		navStr := string(navData)
		if strings.Contains(navStr, `<nav epub:type="toc">`) {
			navStr = strings.Replace(navStr, `<nav epub:type="toc">`, `<nav epub:type="toc" id="toc">`, 1)
		}
		if !strings.Contains(navStr, `epub:type="landmarks"`) {
			landmarks := `    <nav epub:type="landmarks" hidden="">
      <h2>Landmarks</h2>
      <ol>
        <li><a epub:type="toc" href="#toc">Table of Contents</a></li>
      </ol>
    </nav>
  </body>`
			navStr = strings.Replace(navStr, "</body>", landmarks, 1)
		}
		for i := range entries {
			if entries[i].name == navName {
				entries[i].data = []byte(navStr)
				break
			}
		}
	}

	// 5. Write back atomically to temporary file, then rename
	tmpPath := epubPath + ".tmp"
	outFile, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	zw := zip.NewWriter(outFile)

	// In EPUB OCF, mimetype must be the first entry and uncompressed (Method 0)
	for _, e := range entries {
		if e.name == "mimetype" {
			h := &zip.FileHeader{
				Name:   "mimetype",
				Method: zip.Store,
			}
			w, err := zw.CreateHeader(h)
			if err != nil {
				_ = outFile.Close()
				_ = os.Remove(tmpPath)
				return err
			}
			if _, err := w.Write(e.data); err != nil {
				_ = outFile.Close()
				_ = os.Remove(tmpPath)
				return err
			}
			break
		}
	}

	for _, e := range entries {
		if e.name == "mimetype" {
			continue
		}
		h := &zip.FileHeader{
			Name:   e.name,
			Method: zip.Deflate,
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			_ = outFile.Close()
			_ = os.Remove(tmpPath)
			return err
		}
		if _, err := w.Write(e.data); err != nil {
			_ = outFile.Close()
			_ = os.Remove(tmpPath)
			return err
		}
	}

	if err := zw.Close(); err != nil {
		_ = outFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := outFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, epubPath)
}
