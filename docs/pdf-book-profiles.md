# PDF Book Profiles: Auto, Fiction, and Technical

When converting a PDF to EPUB, **rebind** requires layout heuristics to turn visual coordinates into semantic EPUB markup (`<p>`, `<code>`, `<pre>`, `<h1>`, scene breaks, and lists).

PDF documents contain no semantic tags—only 2D coordinates specifying where characters, lines, and images sit on a page canvas. To bridge this gap, rebind uses configurable **Book Profiles** to interpret ambiguous layouts.

---

## The Three Profiles

| Profile | Value | Default | Primary Purpose |
| :--- | :--- | :---: | :--- |
| **Auto-Detect** | `auto` | **Yes** | Inspects font runs and keyword density to choose between Fiction and Technical automatically. |
| **Fiction** | `fiction` | No | Optimizes for novels, memoirs, essays, and storybooks. Prioritizes prose reflow, dialogue, and chapter formatting. |
| **Technical** | `technical` | No | Optimizes for programming, computer science, and engineering books. Prioritizes code blocks, indentation, and listings. |

---

## Why Profiles Are Needed

In a PDF, text like:
```text
return to the castle
```
and:
```go
return nil, err
```
look identical from a raw geometry standpoint: they are both lines of text placed at an `(x, y)` coordinate.

However, in an EPUB:
- The first belongs inside a regular flowing paragraph (`<p>`).
- The second belongs inside a formatted, non-wrapped code block (`<pre><code>`).

The **Book Profile** controls the sensitivity of the state machine so it does not confuse prose with code (or vice versa).

---

## Technical Comparison Under the Hood

### 1. Code Block Recognition (`isCodeStart`)

Located in [`internal/converter/pdf_statemachine.go`](../internal/converter/pdf_statemachine.go):

* **Fiction Mode**:
  - A line is treated as code **only** if the font is explicitly marked as a monospace font family or contains multiple ASCII box-drawing characters (e.g. `┌`, `─`, `│`).
  - Lines starting with words like `class`, `import`, `return`, `var`, or `def` are kept as normal text paragraphs.
* **Technical Mode**:
  - Activates syntax token matching (`func `, `package `, `import `, `def `, `class `, `return `, `const `, `let `, `var `, `if (`, `for (`, `{`, `}`, `//`, `=>`, `->`, `();`).
  - If a line starts with one of these keywords, it triggers a code block even if the PDF embedded a standard proportional font rather than a monospace font.

### 2. Paragraph Fusion and Sentence Continuation (`collectParagraphLines`)

* **Fiction Mode**:
  - Connects lines across line-wraps and page breaks into smooth, uninterrupted paragraphs.
  - Detects dialogue starts (em-dashes `—` and quotation marks `“` or `"`) to ensure speech is preserved as distinct dialogue lines.
  - Formats large initial capitals as drop caps (`.dropcap`) and centers scene breaks (`*  *  *`).
* **Technical Mode**:
  - Prevents greedy paragraph merging near code blocks, numbered algorithm steps, and figure captions.
  - Preserves vertical line pitch and horizontal left-margin indentation for multi-line code examples.

---

## How Auto-Detection Works (`ResolveBookProfile`)

When **Auto-Detect** is selected, rebind scans the document during the inspection phase:

1. **Monospace Font Density**: Calculates the ratio of monospace text runs to total text runs across all pages. If `monoRatio >= 0.025` (2.5%), it resolves to **Technical**.
2. **Technical Captions**: Searches for patterns like `Figure 1.2`, `Listing 3`, `Table 4`. If sufficient captions appear per page ratio, it resolves to **Technical**.
3. **Code Syntax Density**: Evaluates code keyword frequency across all visual lines. If `codeRatio >= 0.04` (4%), it resolves to **Technical**.
4. **Fallback**: If none of these thresholds are met, rebind resolves to **Fiction**.

---

## When to Manually Override

Auto-detection works well for ~90% of books, but manual override is helpful for edge cases:

### Use `technical` explicitly when:
- The book is a technical manual or programming book, but the author used a proportional font (like Helvetica or Times New Roman) for code listings without standard "Listing X" captions.
- Auto-detect treats code blocks as paragraphs, losing indentation and wrapping code into prose sentences.

### Use `fiction` explicitly when:
- The book is a fantasy, sci-fi, or literary novel containing ASCII maps, runic symbols, or character dialogue that triggers false-positive code blocks.
- The novel contains characters discussing technical terms or code snippets, but you want standard book typography throughout.

---

## How to Set the Profile

### In the Interactive TUI:
1. Switch to the **PDF Document** tab using <kbd>Tab</kbd> or <kbd>Ctrl+T</kbd>.
2. Press <kbd>Ctrl+P</kbd> or <kbd>F2</kbd> to cycle between `Auto-Detect`, `Fiction`, and `Technical`.

### In Headless / CLI Mode:
Use the `--profile` flag:

```bash
# Auto-detect (default)
rebind --headless book.pdf

# Force fiction profile
rebind --headless --profile fiction novel.pdf

# Force technical profile
rebind --headless --profile technical programming-guide.pdf
```
