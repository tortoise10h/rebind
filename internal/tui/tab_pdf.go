package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"rebind/internal/converter"
)

// PDFTab represents the UI and state for converting local PDF documents into EPUB.
// It is designed to be easily extensible in the future (e.g. adding a file picker
// or file navigator by embedding the component model and toggling its view).
type PDFTab struct {
	input   textinput.Model
	profile converter.BookProfile

	// Extensibility hook:
	// Future features such as a file picker, directory browser, or file navigator
	// can be stored and managed here without affecting the web converter tab.
}

func newPDFTab(initialPath string, profile converter.BookProfile) PDFTab {
	ti := textinput.New()
	ti.Placeholder = "~/Books/novel.pdf or /path/to/document.pdf"
	ti.CharLimit = 2048
	ti.Width = 64
	ti.Prompt = "PDF > "
	ti.PromptStyle = promptStyle
	if initialPath != "" {
		ti.SetValue(initialPath)
	}
	if profile == "" {
		profile = converter.ProfileAuto
	}
	return PDFTab{
		input:   ti,
		profile: profile,
	}
}

func (t *PDFTab) Focus() tea.Cmd {
	return t.input.Focus()
}

func (t *PDFTab) Blur() {
	t.input.Blur()
}

func (t *PDFTab) Value() string {
	return t.input.Value()
}

func (t *PDFTab) SetValue(v string) {
	t.input.SetValue(v)
}

func (t *PDFTab) Profile() converter.BookProfile {
	return t.profile
}

func (t *PDFTab) SetProfile(p converter.BookProfile) {
	t.profile = p
}

func (t *PDFTab) CycleProfile() {
	switch t.profile {
	case converter.ProfileAuto:
		t.profile = converter.ProfileFiction
	case converter.ProfileFiction:
		t.profile = converter.ProfileTechnical
	default:
		t.profile = converter.ProfileAuto
	}
}

func (t *PDFTab) CycleProfileReverse() {
	switch t.profile {
	case converter.ProfileAuto:
		t.profile = converter.ProfileTechnical
	case converter.ProfileTechnical:
		t.profile = converter.ProfileFiction
	default:
		t.profile = converter.ProfileAuto
	}
}

func (t *PDFTab) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)
	return cmd
}

func (t PDFTab) View() string {
	var b strings.Builder
	b.WriteString(labelStyle.Render("Enter local PDF file path (drag & drop supported):") + "\n\n")
	b.WriteString(t.input.View() + "\n\n")
	b.WriteString(t.renderProfileSelector() + "\n\n")
	b.WriteString(dimStyle.Render("Tip: Drag & drop a PDF file from Finder directly into this window."))
	return b.String()
}

func (t PDFTab) renderProfileSelector() string {
	var autoStr, fictionStr, techStr string

	switch t.profile {
	case converter.ProfileFiction:
		autoStr = dimStyle.Render("○ Auto-Detect")
		fictionStr = activeStepStyle.Render("[● Fiction]")
		techStr = dimStyle.Render("○ Technical")
	case converter.ProfileTechnical:
		autoStr = dimStyle.Render("○ Auto-Detect")
		fictionStr = dimStyle.Render("○ Fiction")
		techStr = activeStepStyle.Render("[● Technical]")
	default:
		autoStr = activeStepStyle.Render("[● Auto-Detect]")
		fictionStr = dimStyle.Render("○ Fiction")
		techStr = dimStyle.Render("○ Technical")
	}

	return fmt.Sprintf(
		"%s  %s   %s   %s\n%s",
		labelStyle.Render("Book Profile:"),
		autoStr,
		fictionStr,
		techStr,
		dimStyle.Render("              (ctrl+p: cycle profile)"),
	)
}
