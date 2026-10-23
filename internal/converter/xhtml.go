package converter

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

var voidElements = map[string]bool{
	"area":   true,
	"base":   true,
	"br":     true,
	"col":    true,
	"embed":  true,
	"hr":     true,
	"img":    true,
	"input":  true,
	"link":   true,
	"meta":   true,
	"param":  true,
	"source": true,
	"track":  true,
	"wbr":    true,
}

var droppedElements = map[string]bool{
	"script":   true,
	"style":    true,
	"noscript": true,
	"iframe":   true,
	"form":     true,
	"button":   true,
	"input":    true,
	"select":   true,
	"textarea": true,
	"object":   true,
	"embed":    true,
	"canvas":   true,
	"audio":    true,
	"video":    true,
	"svg":      true,
}

var allowedAttrs = map[string]bool{
	"href":    true,
	"src":     true,
	"alt":     true,
	"title":   true,
	"id":      true,
	"class":   true,
	"colspan": true,
	"rowspan": true,
	"headers": true,
	"scope":   true,
	"start":   true,
	"type":    true,
	"dir":     true,
	"lang":    true,
}

// ImageReplacer maps an absolute remote image URL to an internal EPUB relative path.
type ImageReplacer func(remoteURL string) (internalPath string, ok bool)

// ExtractImageURLs finds all candidate image URLs in the HTML fragment and resolves them against baseURL.
func ExtractImageURLs(rawHTML string, baseURL *url.URL) []string {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var urls []string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "img") {
			src := resolveImgSrc(n, baseURL)
			if src != "" && !seen[src] && (strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://")) {
				seen[src] = true
				urls = append(urls, src)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return urls
}

// SanitizeToXHTML converts raw HTML from Readability into strict, XML-valid XHTML
// suitable for Apple Books and Kindle, rewriting image paths via replaceImg.
func SanitizeToXHTML(rawHTML string, baseURL *url.URL, replaceImg ImageReplacer) (string, error) {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return "", fmt.Errorf("parse html: %w", err)
	}

	body := findElement(doc, "body")
	if body == nil {
		body = doc
	}

	var buf bytes.Buffer
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		writeXHTMLNode(&buf, c, baseURL, replaceImg)
	}

	out := buf.String()
	if err := validateXHTMLFragment(out); err != nil {
		return "", fmt.Errorf("validate xhtml: %w", err)
	}
	return out, nil
}

func findElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, tag) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func resolveImgSrc(n *html.Node, baseURL *url.URL) string {
	var src, dataSrc, dataLazySrc, srcset string
	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "src":
			src = strings.TrimSpace(a.Val)
		case "data-src", "data-original":
			if dataSrc == "" {
				dataSrc = strings.TrimSpace(a.Val)
			}
		case "data-lazy-src":
			dataLazySrc = strings.TrimSpace(a.Val)
		case "srcset", "data-srcset":
			if srcset == "" {
				srcset = strings.TrimSpace(a.Val)
			}
		}
	}

	candidate := src
	if candidate == "" || strings.HasPrefix(candidate, "data:") {
		if dataSrc != "" {
			candidate = dataSrc
		} else if dataLazySrc != "" {
			candidate = dataLazySrc
		} else if srcset != "" {
			candidate = firstSrcsetURL(srcset)
		}
	}
	if candidate == "" || strings.HasPrefix(candidate, "data:") {
		return ""
	}
	return resolveURL(candidate, baseURL)
}

func firstSrcsetURL(srcset string) string {
	parts := strings.Split(srcset, ",")
	if len(parts) == 0 {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(parts[0]))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func resolveURL(raw string, baseURL *url.URL) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if baseURL != nil {
		u = baseURL.ResolveReference(u)
	}
	return u.String()
}

func writeXHTMLNode(buf *bytes.Buffer, n *html.Node, baseURL *url.URL, replaceImg ImageReplacer) {
	switch n.Type {
	case html.TextNode:
		escapeXMLText(buf, sanitizeXMLChars(n.Data))

	case html.ElementNode:
		tag := strings.ToLower(n.Data)
		if droppedElements[tag] {
			return
		}
		if !isValidXMLName(tag) {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				writeXHTMLNode(buf, c, baseURL, replaceImg)
			}
			return
		}

		if tag == "img" {
			src := resolveImgSrc(n, baseURL)
			if src == "" {
				return
			}
			if replaceImg != nil {
				if localPath, ok := replaceImg(src); ok && localPath != "" {
					src = localPath
				} else {
					// Skip unbundled remote images so offline Apple Books never shows broken image icons
					return
				}
			}
			alt := getAttr(n, "alt")
			buf.WriteString("<img src=\"")
			escapeXMLAttr(buf, sanitizeXMLChars(src))
			buf.WriteString("\" alt=\"")
			escapeXMLAttr(buf, sanitizeXMLChars(alt))
			buf.WriteString("\"/>")
			return
		}

		// Unwrap <a> tag if it has no destination href or id/name anchor
		if tag == "a" && getAttr(n, "href") == "" && getAttr(n, "id") == "" && getAttr(n, "name") == "" {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				writeXHTMLNode(buf, c, baseURL, replaceImg)
			}
			return
		}

		buf.WriteByte('<')
		buf.WriteString(tag)

		seenAttr := make(map[string]bool)
		for _, a := range n.Attr {
			if a.Namespace != "" {
				continue
			}
			key := strings.ToLower(a.Key)
			if !allowedAttrs[key] || seenAttr[key] || !isValidXMLName(key) {
				continue
			}
			val := a.Val
			if key == "href" {
				resolved := resolveURL(val, baseURL)
				if resolved == "" {
					continue
				}
				val = resolved
			}
			seenAttr[key] = true
			buf.WriteByte(' ')
			buf.WriteString(key)
			buf.WriteString("=\"")
			escapeXMLAttr(buf, sanitizeXMLChars(val))
			buf.WriteByte('"')
		}

		if voidElements[tag] {
			buf.WriteString("/>")
			return
		}

		buf.WriteByte('>')
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeXHTMLNode(buf, c, baseURL, replaceImg)
		}
		buf.WriteString("</")
		buf.WriteString(tag)
		buf.WriteByte('>')

	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeXHTMLNode(buf, c, baseURL, replaceImg)
		}
	}
}

func getAttr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

func setAttr(n *html.Node, name, val string) {
	for i := range n.Attr {
		if strings.EqualFold(n.Attr[i].Key, name) {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: name, Val: val})
}

func removeAttr(n *html.Node, name string) {
	newAttrs := make([]html.Attribute, 0, len(n.Attr))
	for _, a := range n.Attr {
		if !strings.EqualFold(a.Key, name) {
			newAttrs = append(newAttrs, a)
		}
	}
	n.Attr = newAttrs
}

func isValidXMLName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !unicode.IsLetter(r) && r != '_' {
				return false
			}
		} else {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' {
				return false
			}
		}
	}
	return true
}

func sanitizeXMLChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r == 0x09 || r == 0x0A || r == 0x0D ||
			(r >= 0x20 && r <= 0xD7FF) ||
			(r >= 0xE000 && r <= 0xFFFD) ||
			(r >= 0x10000 && r <= 0x10FFFF) {
			return r
		}
		return -1
	}, s)
}

func escapeXMLText(buf *bytes.Buffer, s string) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			buf.WriteString("&amp;")
		case '<':
			buf.WriteString("&lt;")
		case '>':
			buf.WriteString("&gt;")
		default:
			buf.WriteByte(s[i])
		}
	}
}

func escapeXMLAttr(buf *bytes.Buffer, s string) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			buf.WriteString("&amp;")
		case '<':
			buf.WriteString("&lt;")
		case '>':
			buf.WriteString("&gt;")
		case '"':
			buf.WriteString("&quot;")
		case '\'':
			buf.WriteString("&#39;")
		case '\n':
			buf.WriteString("&#10;")
		case '\r':
			buf.WriteString("&#13;")
		case '\t':
			buf.WriteString("&#9;")
		default:
			buf.WriteByte(s[i])
		}
	}
}

func validateXHTMLFragment(fragment string) error {
	wrapped := "<div>" + fragment + "</div>"
	dec := xml.NewDecoder(strings.NewReader(wrapped))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return nil
			}
			return err
		}
	}
}
