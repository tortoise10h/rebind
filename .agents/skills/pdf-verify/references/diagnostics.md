# PDF Pipeline Diagnostic Guide

## Pipeline Stages & Inspection Commands

`cmd/verify inspect` allows inspecting any specific stage of the pipeline without modifying source files or running the full conversion.

```bash
# Stage options: all | xml | lines | blocks | xhtml
go run ./cmd/verify inspect --pdf <pdf> --pages <range> --stage <stage>
```

### Stage 1: `xml` (Poppler Raw XML Output)
Displays `<text>` and `<image>` runs directly as reported by `pdftohtml -xml`:
- Coordinates: `Top`, `Left`, `Width`, `Height`, `Bottom=Top+Height`
- Typography: `FontSize`, `FontFamily`, `FontID`
- Text content: raw `InnerXML`

Use this stage when:
- Investigating font sizes, baseline alignments, or drop-cap bounding boxes.
- Verifying whether an image is present in the source PDF and its exact dimensions.

### Stage 2: `lines` (Visual Line Clustering & Noise Stripping)
Displays:
- `RawLines`: lines formed by clustering text runs on shared baselines before running noise removal.
- `Stripped Noise`: running headers, footers, and page numbers detected and stripped.
- `CleanLines`: surviving lines passed to the layout state machine.

Use this stage when:
- Running headers/footers leaked into body text.
- Valid body text was accidentally stripped as a header or footer.
- Drops caps clustered incorrectly with body lines.

### Stage 3: `blocks` (Layout State Machine Classification)
Displays categorized semantic blocks:
- `Heading1`, `Heading2`, `Heading3`
- `Paragraph`
- `Figure`
- `Table`
- `Code`
- `SceneBreak`
- `List`

Use this stage when:
- Inspecting whether a paragraph split occurred prematurely.
- Checking heading hierarchy and title coalescing.
- Confirming that table or vector diagram recovery triggered.

### Stage 4: `xhtml` (Rendered XHTML Output)
Displays the exact XHTML markup for the requested pages.

Use this stage when:
- Checking drop-cap formatting (`<span class="dropcap">X</span>`).
- Checking link destinations (`<a href="...">`).
- Inspecting page anchor insertion (`<span id="p%d"></span>`).
