package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"rebind/internal/converter"
)

func TestNewModel_Default(t *testing.T) {
	m := NewModel("", "/tmp/output")

	if m.ActiveTab() != TabWeb {
		t.Fatalf("expected active tab to be TabWeb, got %v", m.ActiveTab())
	}
	if m.WebInput() != "" {
		t.Errorf("expected empty web input, got %q", m.WebInput())
	}
	if m.PDFInput() != "" {
		t.Errorf("expected empty pdf input, got %q", m.PDFInput())
	}
	if m.Profile() != converter.ProfileAuto {
		t.Errorf("expected default profile ProfileAuto, got %q", m.Profile())
	}
}

func TestNewModel_WithWebURL(t *testing.T) {
	url := "https://example.com/blog/article"
	m := NewModel(url, "/tmp/output")

	if m.ActiveTab() != TabWeb {
		t.Fatalf("expected active tab TabWeb for URL, got %v", m.ActiveTab())
	}
	if m.WebInput() != url {
		t.Errorf("expected web input %q, got %q", url, m.WebInput())
	}
}

func TestNewModel_WithPDFPath(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"relative pdf", "documents/sample.pdf"},
		{"absolute pdf", "/Users/user/Books/novel.pdf"},
		{"home tilde pdf", "~/Books/guide.pdf"},
		{"quoted pdf", "'~/Books/guide.pdf'"},
		{"file uri", "file:///path/to/book.pdf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel(tt.input, "/tmp/output")
			if m.ActiveTab() != TabPDF {
				t.Fatalf("expected active tab TabPDF for %q, got %v", tt.input, m.ActiveTab())
			}
			if m.PDFInput() != tt.input {
				t.Errorf("expected pdf input %q, got %q", tt.input, m.PDFInput())
			}
		})
	}
}

func TestTabSwitching(t *testing.T) {
	m := NewModel("", "/tmp/output")

	if m.ActiveTab() != TabWeb {
		t.Fatalf("expected initial tab TabWeb, got %v", m.ActiveTab())
	}

	// Switch to PDF tab via "tab" key
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newM.(Model)
	if m.ActiveTab() != TabPDF {
		t.Fatalf("expected tab to switch to TabPDF, got %v", m.ActiveTab())
	}

	// Switch back to Web tab via "tab" key
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newM.(Model)
	if m.ActiveTab() != TabWeb {
		t.Fatalf("expected tab to switch to TabWeb, got %v", m.ActiveTab())
	}

	// Switch via "shift+tab"
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = newM.(Model)
	if m.ActiveTab() != TabPDF {
		t.Fatalf("expected tab to switch to TabPDF via shift+tab, got %v", m.ActiveTab())
	}

	// Switch via "ctrl+t"
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = newM.(Model)
	if m.ActiveTab() != TabWeb {
		t.Fatalf("expected tab to switch to TabWeb via ctrl+t, got %v", m.ActiveTab())
	}
}

func TestTabInputIsolation(t *testing.T) {
	m := NewModel("", "/tmp/output")

	// Type into Web tab
	m.webTab.SetValue("https://charm.sh")
	if m.WebInput() != "https://charm.sh" {
		t.Errorf("expected charm.sh in Web tab, got %q", m.WebInput())
	}
	if m.PDFInput() != "" {
		t.Errorf("expected empty PDF tab, got %q", m.PDFInput())
	}

	// Switch to PDF tab
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newM.(Model)

	// Type into PDF tab
	m.pdfTab.SetValue("/tmp/novel.pdf")
	if m.PDFInput() != "/tmp/novel.pdf" {
		t.Errorf("expected novel.pdf in PDF tab, got %q", m.PDFInput())
	}
	// Web tab input remains intact
	if m.WebInput() != "https://charm.sh" {
		t.Errorf("expected Web tab to retain https://charm.sh, got %q", m.WebInput())
	}
}

func TestPDFProfileCycling(t *testing.T) {
	m := NewModel("", "/tmp/output")

	// On Web tab, ctrl+p should not cycle profile
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = newM.(Model)
	if m.Profile() != converter.ProfileAuto {
		t.Errorf("expected profile still ProfileAuto on web tab, got %q", m.Profile())
	}

	// Switch to PDF tab
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newM.(Model)

	// Cycle 1: Auto -> Fiction
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = newM.(Model)
	if m.Profile() != converter.ProfileFiction {
		t.Errorf("expected ProfileFiction, got %q", m.Profile())
	}

	// Cycle 2: Fiction -> Technical
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = newM.(Model)
	if m.Profile() != converter.ProfileTechnical {
		t.Errorf("expected ProfileTechnical, got %q", m.Profile())
	}

	// Cycle 3: Technical -> Auto
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = newM.(Model)
	if m.Profile() != converter.ProfileAuto {
		t.Errorf("expected ProfileAuto, got %q", m.Profile())
	}

	// F2 key also cycles profile
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyF2})
	m = newM.(Model)
	if m.Profile() != converter.ProfileFiction {
		t.Errorf("expected ProfileFiction via F2, got %q", m.Profile())
	}
}

func TestViewRendering(t *testing.T) {
	m := NewModel("", "/tmp/output")

	// Web tab view
	viewWeb := m.View()
	if !strings.Contains(viewWeb, "1. Web Article") {
		t.Errorf("expected view to contain tab 1 header")
	}
	if !strings.Contains(viewWeb, "2. PDF Document") {
		t.Errorf("expected view to contain tab 2 header")
	}
	if !strings.Contains(viewWeb, "Paste a web article") {
		t.Errorf("expected web prompt in view")
	}

	// Switch to PDF tab view
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newM.(Model)
	viewPDF := m.View()

	if !strings.Contains(viewPDF, "Enter local PDF file path") {
		t.Errorf("expected PDF prompt in view")
	}
	if !strings.Contains(viewPDF, "Book Profile:") {
		t.Errorf("expected Book Profile selector in view")
	}
	if !strings.Contains(viewPDF, "Auto-Detect") {
		t.Errorf("expected Auto-Detect radio option in view")
	}
}

func TestResetToInput(t *testing.T) {
	m := NewModel("https://example.com", "/tmp/output")
	if m.WebInput() != "https://example.com" {
		t.Fatalf("expected initial URL")
	}

	// Reset with clear
	m.resetToInput(true)
	if m.WebInput() != "" {
		t.Errorf("expected cleared web input, got %q", m.WebInput())
	}

	// Switch to PDF tab
	m.switchTab(TabPDF)
	m.pdfTab.SetValue("/tmp/test.pdf")

	// Reset with clear on PDF tab
	m.resetToInput(true)
	if m.PDFInput() != "" {
		t.Errorf("expected cleared PDF input, got %q", m.PDFInput())
	}
}

func TestValidateWebInput(t *testing.T) {
	valid := []string{
		"https://example.com/article",
		"http://blog.golang.org/go1.22",
		"example.com/post",
	}
	for _, input := range valid {
		if err := validateWebInput(input); err != nil {
			t.Errorf("expected %q to be valid web input, got error: %v", input, err)
		}
	}

	invalid := []struct {
		input       string
		errContains string
	}{
		{"", "please enter a web URL"},
		{"/path/to/doc.pdf", "Web tab only accepts web article URLs"},
		{"sample.pdf", "Web tab only accepts web article URLs"},
		{"https://example.com/doc.pdf", "Web tab only accepts web article URLs"},
		{"file:///Users/me/book.pdf", "Web tab only accepts web article URLs"},
		{"justwords", "invalid web URL"},
	}
	for _, tt := range invalid {
		err := validateWebInput(tt.input)
		if err == nil {
			t.Errorf("expected error for %q, got nil", tt.input)
		} else if !strings.Contains(err.Error(), tt.errContains) {
			t.Errorf("for %q expected error containing %q, got %q", tt.input, tt.errContains, err.Error())
		}
	}
}

func TestValidatePDFInput(t *testing.T) {
	valid := []string{
		"/Users/user/doc.pdf",
		"sample.pdf",
		"~/Downloads/book.pdf",
		"'~/Downloads/book.pdf'",
		"file:///tmp/doc.pdf",
		"https://example.com/book.pdf",
	}
	for _, input := range valid {
		if err := validatePDFInput(input); err != nil {
			t.Errorf("expected %q to be valid PDF input, got error: %v", input, err)
		}
	}

	invalid := []struct {
		input       string
		errContains string
	}{
		{"", "please enter a PDF file path"},
		{"https://example.com/article", "PDF tab only accepts PDF file paths"},
		{"http://blog.com/post", "PDF tab only accepts PDF file paths"},
		{"hello", "invalid PDF path"},
		{"not-a-pdf.txt", "invalid PDF path"},
	}
	for _, tt := range invalid {
		err := validatePDFInput(tt.input)
		if err == nil {
			t.Errorf("expected error for %q, got nil", tt.input)
		} else if !strings.Contains(err.Error(), tt.errContains) {
			t.Errorf("for %q expected error containing %q, got %q", tt.input, tt.errContains, err.Error())
		}
	}
}

func TestInputValidationOnEnter(t *testing.T) {
	// Web tab rejects PDF on Enter
	m := NewModel("", "/tmp/output")
	m.webTab.SetValue("/tmp/novel.pdf")
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newM.(Model)
	if m.state != stateError {
		t.Fatalf("expected stateError when pressing enter with PDF on web tab, got %v", m.state)
	}
	if !strings.Contains(m.err.Error(), "Web tab only accepts web article URLs") {
		t.Errorf("expected error message to guide user to PDF tab, got %q", m.err.Error())
	}

	// PDF tab rejects Web URL on Enter
	m = NewModel("", "/tmp/output")
	m.switchTab(TabPDF)
	m.pdfTab.SetValue("https://example.com/article")
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newM.(Model)
	if m.state != stateError {
		t.Fatalf("expected stateError when pressing enter with Web URL on PDF tab, got %v", m.state)
	}
	if !strings.Contains(m.err.Error(), "PDF tab only accepts PDF file paths") {
		t.Errorf("expected error message to guide user to Web tab, got %q", m.err.Error())
	}
}
