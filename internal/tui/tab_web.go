package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// WebTab represents the UI and state for converting web articles into EPUB.
type WebTab struct {
	input textinput.Model
}

func newWebTab(initialURL string) WebTab {
	ti := textinput.New()
	ti.Placeholder = "https://example.com/article"
	ti.CharLimit = 2048
	ti.Width = 64
	ti.Prompt = "URL > "
	ti.PromptStyle = promptStyle
	if initialURL != "" {
		ti.SetValue(initialURL)
	}
	return WebTab{input: ti}
}

func (t *WebTab) Focus() tea.Cmd {
	return t.input.Focus()
}

func (t *WebTab) Blur() {
	t.input.Blur()
}

func (t *WebTab) Value() string {
	return t.input.Value()
}

func (t *WebTab) SetValue(v string) {
	t.input.SetValue(v)
}

func (t *WebTab) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)
	return cmd
}

func (t WebTab) View() string {
	var b strings.Builder
	b.WriteString(labelStyle.Render("Paste a web article or blog post URL:") + "\n\n")
	b.WriteString(t.input.View() + "\n\n")
	b.WriteString(dimStyle.Render("Supported: Web articles, blog posts, essays, and online documentation."))
	return b.String()
}
