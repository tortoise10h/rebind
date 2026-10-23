# PDF Verification Checklist

Use this checklist whenever evaluating an EPUB converted by `rebind`:

## 1. Automated Verification Gate
- [ ] Run `go run ./cmd/verify audit --pdf <source.pdf> --epub <output.epub> --strict`
- [ ] Ensure all 6 audit categories pass with `0` defects.

## 2. Chapter Openings (Drop Caps)
- [ ] Chapter 1 opens with `<p><span class="dropcap">I</span>t was nearing midnight...` without stray spaces (`I t was`) or line 3 text merged into the drop cap.
- [ ] Chapter 28 (p.611) drop cap `<span class="dropcap">H</span>arry felt...` breaks properly before dialogue (`“Out of here, quickly,” said Snape.`).
- [ ] Chapter 29 (p.625) drop cap `<span class="dropcap">C</span>’mere, Harry . . .”` retains quotation mark and apostrophe.

## 3. Image & Artwork Integrity
- [ ] Front cover is set (`properties="cover-image"` in manifest, pointing to full-bleed front cover, not narrow spine flap).
- [ ] No micro-strip spam (sliced artwork on title page collapsed into single composite image).
- [ ] Chapter illustrations appear between chapter number and subtitle/title, not above chapter number.

## 4. Reading Flow & Continuity
- [ ] Sentences wrapping across page boundaries are reflowed into single `<p>` blocks with `<span id="pN"></span>`.
- [ ] No mid-sentence paragraph breaks or first-line indentation gaps at page transitions.
- [ ] Trailing hyphens across page breaks are de-hyphenated using document lexicon.

## 5. TOC & Headings
- [ ] At most one `<h1>` per chapter file.
- [ ] TOC titles clean without print page number suffixes (e.g. `The Other Minister` instead of `The Other Minister · 1`).
- [ ] No shifted Roman numeral ghost chapters (`chapter_034` titled `vii`) at end of book.
