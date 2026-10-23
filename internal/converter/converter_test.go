package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestConvertEndToEnd(t *testing.T) {
	// Generate a valid 2x2 PNG image in memory
	var pngBuf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 108, G: 92, B: 231, A: 255})
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	pngBytes := pngBuf.Bytes()

	mux := http.NewServeMux()
	mux.HandleFunc("/images/diagram.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	mux.HandleFunc("/blog/deep-dive-into-go", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!DOCTYPE html>
<html>
<head>
	<title>A Deep Dive into Go Concurrency</title>
	<meta name="author" content="Jane Doe">
</head>
<body>
	<nav>Navigation Links to Strip</nav>
	<article>
		<h1>A Deep Dive into Go Concurrency</h1>
		<p>Go makes concurrent programming approachable with goroutines &amp; channels.&nbsp;Here is why it works so well in practice.</p>
		<p>When building pipelines, you often combine multiple stages:<br>First fetch data, then transform it, and finally package the output.<hr></p>
		<figure>
			<img src="/images/diagram.png" alt="Architecture diagram">
			<figcaption>Figure 1: Pipeline overview</figcaption>
		</figure>
		<pre><code>go func() {
    ch &lt;- result
}()</code></pre>
		<p>This second paragraph ensures Readability recognizes this section as a full-length blog article suitable for extraction and conversion into an e-book.</p>
	</article>
	<footer>Footer clutter</footer>
</body>
</html>`)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	outDir := t.TempDir()
	res, err := Convert(context.Background(), srv.URL+"/blog/deep-dive-into-go", outDir, nil)
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}

	if res.Title != "A Deep Dive into Go Concurrency" {
		t.Errorf("unexpected title: %q", res.Title)
	}
	if res.ImagesSaved != 1 {
		t.Errorf("expected 1 saved image, got %d (of %d)", res.ImagesSaved, res.ImagesTotal)
	}
	if _, err := os.Stat(res.OutputPath); err != nil {
		t.Fatalf("output epub does not exist: %v", err)
	}

	// Open generated .epub (zip archive) and verify strict XML validity of every .xhtml file
	zr, err := zip.OpenReader(res.OutputPath)
	if err != nil {
		t.Fatalf("open epub zip: %v", err)
	}
	defer zr.Close()

	var foundArticleXHTML, foundImage, foundCSS bool
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "article.xhtml") {
			foundArticleXHTML = true
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open %s: %v", f.Name, err)
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatalf("read %s: %v", f.Name, err)
			}

			// Parse with strict standard library XML decoder
			dec := xml.NewDecoder(bytes.NewReader(data))
			for {
				if _, err := dec.Token(); err != nil {
					if err == io.EOF {
						break
					}
					t.Fatalf("strict XML validation failed on %s: %v\nContent:\n%s", f.Name, err, string(data))
				}
			}
		}
		if strings.Contains(f.Name, "images/img_0001.png") {
			foundImage = true
		}
		if strings.Contains(f.Name, "css/style.css") {
			foundCSS = true
		}
	}

	if !foundArticleXHTML {
		t.Errorf("expected article.xhtml in EPUB archive")
	}
	if !foundImage {
		t.Errorf("expected bundled image in EPUB archive")
	}
	if !foundCSS {
		t.Errorf("expected style.css in EPUB archive")
	}
}
