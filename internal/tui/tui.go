package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"rebind/internal/converter"
)

type state int

const (
	stateInput state = iota
	stateConverting
	stateDone
	stateError
)

// Tab represents the active converter view mode in the TUI.
type Tab int

const (
	TabWeb Tab = iota
	TabPDF
)

func (t Tab) String() string {
	switch t {
	case TabWeb:
		return "Web Article"
	case TabPDF:
		return "PDF Document"
	default:
		return "Unknown"
	}
}

type progressMsg converter.ProgressEvent

type doneMsg struct {
	result *converter.Result
}

type errMsg struct {
	err error
}

type actionStatusMsg struct {
	text string
}

type pipelineStep struct {
	step  converter.Step
	label string
}

var pipelineSteps = []pipelineStep{
	{converter.StepFetching, "Fetch web page HTML"},
	{converter.StepExtracting, "Extract article via Readability engine"},
	{converter.StepLocalizingAssets, "Download images & sanitize strict XHTML"},
	{converter.StepPackaging, "Package EPUB with reader stylesheet"},
}

var pdfPipelineSteps = []pipelineStep{
	{converter.StepFetching, "Inspect PDF & extract spatial layout via Poppler"},
	{converter.StepExtracting, "Remove headers/footers & reconstruct reading flow"},
	{converter.StepLocalizingAssets, "Render figures, code blocks, tables & validate XHTML"},
	{converter.StepPackaging, "Package multi-chapter EPUB with reader stylesheet"},
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#6C5CE7")).
			Padding(0, 1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0B0"))

	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#6C5CE7")).
			Padding(0, 2)

	inactiveTabStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8E8EA0")).
			Background(lipgloss.Color("#25233A")).
			Padding(0, 2)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#6C5CE7")).
			Padding(1, 2).
			Width(74)

	successBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#00B894")).
			Padding(1, 2).
			Width(74)

	errorBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#FF7675")).
			Padding(1, 2).
			Width(74)

	labelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#DCDCE6"))

	promptStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#A29BFE"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6C6C80"))

	successStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00B894"))

	activeStepStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#A29BFE"))

	errorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FF7675"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7F8C8D")).
			MarginTop(1)
)

// Model is the Bubble Tea model for the rebind TUI.
type Model struct {
	state           state
	activeTab       Tab
	webTab          WebTab
	pdfTab          PDFTab
	spinner         spinner.Model
	outputDir       string
	isPDF           bool
	convertingInput string
	currentStep     converter.Step
	statusMessage   string
	result          *converter.Result
	err             error
	actionStatus    string
	progressCh      chan converter.ProgressEvent
}

// NewModel initializes a new TUI model.
func NewModel(initialURL string, outputDir string) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#A29BFE"))

	var activeTab Tab
	var webInitial, pdfInitial string

	if initialURL != "" {
		if isPDFInput(initialURL) {
			activeTab = TabPDF
			pdfInitial = initialURL
		} else {
			activeTab = TabWeb
			webInitial = initialURL
		}
	} else {
		activeTab = TabWeb
	}

	webTab := newWebTab(webInitial)
	pdfTab := newPDFTab(pdfInitial, converter.ProfileAuto)

	if activeTab == TabWeb {
		webTab.Focus()
	} else {
		pdfTab.Focus()
	}

	return Model{
		state:     stateInput,
		activeTab: activeTab,
		webTab:    webTab,
		pdfTab:    pdfTab,
		spinner:   sp,
		outputDir: outputDir,
	}
}

// ActiveTab returns the currently active tab.
func (m Model) ActiveTab() Tab {
	return m.activeTab
}

// WebInput returns the current value of the web URL input.
func (m Model) WebInput() string {
	return m.webTab.Value()
}

// PDFInput returns the current value of the PDF path input.
func (m Model) PDFInput() string {
	return m.pdfTab.Value()
}

// Profile returns the current book profile for PDF conversions.
func (m Model) Profile() converter.BookProfile {
	return m.pdfTab.Profile()
}

func isPDFInput(val string) bool {
	trimmed := strings.TrimSpace(val)
	if (strings.HasPrefix(trimmed, "'") && strings.HasSuffix(trimmed, "'") && len(trimmed) >= 2) ||
		(strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2) {
		trimmed = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	}
	lower := strings.ToLower(trimmed)
	if strings.HasSuffix(lower, ".pdf") {
		return true
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "file://") {
		return true
	}
	return false
}

func validateWebInput(val string) error {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return fmt.Errorf("please enter a web URL")
	}

	// Reject local files or PDF files on the Web tab
	if isPDFInput(trimmed) || strings.HasSuffix(strings.ToLower(trimmed), ".pdf") {
		return fmt.Errorf("Web tab only accepts web article URLs.\nPress Tab to switch to the PDF tab for %q", trimmed)
	}

	normalized, parsed, err := converter.NormalizeURL(trimmed)
	if err != nil {
		return fmt.Errorf("invalid web URL: %w", err)
	}

	if !strings.Contains(parsed.Host, ".") && parsed.Host != "localhost" {
		return fmt.Errorf("invalid web URL: %q (expected http:// or https:// domain)", normalized)
	}

	return nil
}

func validatePDFInput(val string) error {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return fmt.Errorf("please enter a PDF file path")
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if !strings.HasSuffix(lower, ".pdf") {
			return fmt.Errorf("PDF tab only accepts PDF file paths.\nPress Tab to switch to the Web tab for %q", trimmed)
		}
	}

	clean := trimmed
	if (strings.HasPrefix(clean, "'") && strings.HasSuffix(clean, "'") && len(clean) >= 2) ||
		(strings.HasPrefix(clean, "\"") && strings.HasSuffix(clean, "\"") && len(clean) >= 2) {
		clean = strings.TrimSpace(clean[1 : len(clean)-1])
	}

	if !isPDFInput(clean) {
		return fmt.Errorf("invalid PDF path: %q (expected a .pdf file)", trimmed)
	}

	return nil
}

func (m Model) Init() tea.Cmd {
	if m.activeTab == TabWeb {
		return tea.Batch(textinput.Blink, m.webTab.Focus())
	}
	return tea.Batch(textinput.Blink, m.pdfTab.Focus())
}

func (m *Model) switchTab(tab Tab) tea.Cmd {
	if m.activeTab == tab {
		return nil
	}
	m.activeTab = tab
	if tab == TabWeb {
		m.pdfTab.Blur()
		return m.webTab.Focus()
	}
	m.webTab.Blur()
	return m.pdfTab.Focus()
}

func (m *Model) toggleTab() tea.Cmd {
	if m.activeTab == TabWeb {
		return m.switchTab(TabPDF)
	}
	return m.switchTab(TabWeb)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.state == stateConverting {
				return m, tea.Quit
			}
			if m.state == stateDone || m.state == stateError {
				cmd := m.resetToInput(m.state == stateDone)
				return m, cmd
			}
			return m, tea.Quit
		}

		switch m.state {
		case stateInput:
			switch msg.String() {
			case "enter":
				var (
					raw      string
					validate func(string) error
				)

				switch m.activeTab {
				case TabPDF:
					raw = strings.TrimSpace(m.pdfTab.Value())
					validate = validatePDFInput
				case TabWeb:
					raw = strings.TrimSpace(m.webTab.Value())
					validate = validateWebInput
				}

				if raw == "" {
					return m, nil
				}
				if err := validate(raw); err != nil {
					m.state = stateError
					m.err = err
					return m, nil
				}
				return m.startConversion(raw)

			case "tab", "shift+tab", "ctrl+t":
				cmd := m.toggleTab()
				return m, cmd

			case "ctrl+p", "f2":
				if m.activeTab == TabPDF {
					m.pdfTab.CycleProfile()
					return m, nil
				}
			}

		case stateDone:
			switch msg.String() {
			case "q":
				return m, tea.Quit
			case "enter", "n":
				cmd := m.resetToInput(true)
				return m, cmd
			case "o":
				if m.result != nil {
					return m, openInAppleBooksCmd(m.result.OutputPath)
				}
			case "r":
				if m.result != nil {
					return m, revealInFinderCmd(m.result.OutputPath)
				}
			}

		case stateError:
			switch msg.String() {
			case "q":
				return m, tea.Quit
			case "enter", "r":
				cmd := m.resetToInput(false)
				return m, cmd
			}
		}

	case spinner.TickMsg:
		if m.state == stateConverting {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

	case progressMsg:
		m.currentStep = msg.Step
		m.statusMessage = msg.Message
		return m, waitForProgressCmd(m.progressCh)

	case doneMsg:
		m.state = stateDone
		m.result = msg.result
		m.currentStep = converter.StepComplete
		return m, nil

	case errMsg:
		m.state = stateError
		m.err = msg.err
		return m, nil

	case actionStatusMsg:
		m.actionStatus = msg.text
		return m, nil
	}

	if m.state == stateInput {
		var cmd tea.Cmd
		if m.activeTab == TabWeb {
			cmd = m.webTab.Update(msg)
		} else {
			cmd = m.pdfTab.Update(msg)
		}
		return m, cmd
	}

	return m, nil
}

func (m *Model) resetToInput(clearValue bool) tea.Cmd {
	m.state = stateInput
	m.err = nil
	m.result = nil
	m.actionStatus = ""
	m.currentStep = 0
	m.statusMessage = ""
	if clearValue {
		if m.activeTab == TabWeb {
			m.webTab.SetValue("")
		} else {
			m.pdfTab.SetValue("")
		}
	}
	if m.activeTab == TabWeb {
		return m.webTab.Focus()
	}
	return m.pdfTab.Focus()
}

func (m Model) startConversion(rawInput string) (tea.Model, tea.Cmd) {
	m.state = stateConverting
	m.err = nil
	m.result = nil
	m.actionStatus = ""
	m.currentStep = converter.StepFetching
	m.statusMessage = "Starting conversion..."
	m.isPDF = (m.activeTab == TabPDF)
	m.convertingInput = rawInput
	m.webTab.Blur()
	m.pdfTab.Blur()

	ch := make(chan converter.ProgressEvent, 16)
	m.progressCh = ch
	outDir := m.outputDir
	profile := m.pdfTab.profile

	runCmd := func() tea.Msg {
		defer close(ch)
		res, err := converter.ConvertWithOptions(context.Background(), rawInput, converter.ConvertOptions{
			OutputDir: outDir,
			Profile:   profile,
		}, func(ev converter.ProgressEvent) {
			select {
			case ch <- ev:
			default:
			}
		})
		if err != nil {
			return errMsg{err: err}
		}
		return doneMsg{result: res}
	}

	return m, tea.Batch(
		m.spinner.Tick,
		waitForProgressCmd(ch),
		runCmd,
	)
}

func waitForProgressCmd(ch <-chan converter.ProgressEvent) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return progressMsg(ev)
	}
}

func openInAppleBooksCmd(epubPath string) tea.Cmd {
	return func() tea.Msg {
		if err := exec.Command("open", "-a", "Books", epubPath).Start(); err != nil {
			return actionStatusMsg{text: fmt.Sprintf("Could not open Apple Books: %v", err)}
		}
		return actionStatusMsg{text: "Opened in Apple Books!"}
	}
}

func revealInFinderCmd(epubPath string) tea.Cmd {
	return func() tea.Msg {
		if err := exec.Command("open", "-R", epubPath).Start(); err != nil {
			return actionStatusMsg{text: fmt.Sprintf("Could not open Finder: %v", err)}
		}
		return actionStatusMsg{text: "Revealed in Finder!"}
	}
}

func (m Model) renderTabBar() string {
	var webTabRendered, pdfTabRendered string

	if m.activeTab == TabWeb {
		webTabRendered = activeTabStyle.Render("1. Web Article")
		pdfTabRendered = inactiveTabStyle.Render("2. PDF Document")
	} else {
		webTabRendered = inactiveTabStyle.Render("1. Web Article")
		pdfTabRendered = activeTabStyle.Render("2. PDF Document")
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, webTabRendered, "  ", pdfTabRendered)
}

func (m Model) View() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("  " + titleStyle.Render("rebind") + "  " + subtitleStyle.Render("Web Article & PDF to EPUB Converter (Apple Books / Kindle)"))
	b.WriteString("\n")
	b.WriteString("  " + dimStyle.Render("Output directory: "+m.outputDir))
	b.WriteString("\n\n")

	switch m.state {
	case stateInput:
		tabsBar := m.renderTabBar()
		var tabContent string
		var helpText string

		if m.activeTab == TabWeb {
			tabContent = m.webTab.View()
			helpText = "enter: convert to epub • tab: switch to PDF • esc / ctrl+c: quit"
		} else {
			tabContent = m.pdfTab.View()
			helpText = "enter: convert to epub • tab: switch to Web • ctrl+p: cycle profile • esc / ctrl+c: quit"
		}

		b.WriteString("  " + tabsBar + "\n")
		b.WriteString("  " + indentMultiline(boxStyle.Render(tabContent), "  "))
		b.WriteString("\n")
		b.WriteString("  " + helpStyle.Render(helpText))

	case stateConverting:
		var stepsView strings.Builder
		stepsView.WriteString(labelStyle.Render("Converting: ") + dimStyle.Render(truncate(m.convertingInput, 54)) + "\n\n")

		steps := pipelineSteps
		if m.isPDF {
			steps = pdfPipelineSteps
		}

		for _, s := range steps {
			switch {
			case m.currentStep > s.step:
				stepsView.WriteString(fmt.Sprintf("  %s %s\n", successStyle.Render("✓"), s.label))
			case m.currentStep == s.step:
				stepsView.WriteString(fmt.Sprintf("  %s %s\n", m.spinner.View(), activeStepStyle.Render(s.label)))
			default:
				stepsView.WriteString(fmt.Sprintf("  %s %s\n", dimStyle.Render("•"), dimStyle.Render(s.label)))
			}
		}

		if m.statusMessage != "" {
			stepsView.WriteString("\n" + dimStyle.Render(m.statusMessage))
		}

		b.WriteString("  " + indentMultiline(boxStyle.Render(stepsView.String()), "  "))
		b.WriteString("\n")
		b.WriteString("  " + helpStyle.Render("ctrl+c: cancel & quit"))

	case stateDone:
		res := m.result
		author := res.Author
		if author == "" {
			author = "Unknown"
		}
		var summary strings.Builder
		summary.WriteString(successStyle.Render("✓ EPUB Generated Successfully!") + "\n\n")
		summary.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Title:  "), res.Title))
		summary.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Author: "), author))
		if res.PagesTotal > 0 {
			summary.WriteString(fmt.Sprintf("%s %d pages → %d chapters (profile: %s)\n", labelStyle.Render("Format: "), res.PagesTotal, res.Chapters, res.ProfileUsed))
		}
		summary.WriteString(fmt.Sprintf("%s %d bundled (of %d found)\n", labelStyle.Render("Images: "), res.ImagesSaved, res.ImagesTotal))
		summary.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Saved:  "), res.OutputPath))
		summary.WriteString(fmt.Sprintf("%s %s", labelStyle.Render("Time:   "), res.Duration.Round(10*1e6)))

		if m.actionStatus != "" {
			summary.WriteString("\n\n" + activeStepStyle.Render("→ "+m.actionStatus))
		}

		b.WriteString("  " + indentMultiline(successBoxStyle.Render(summary.String()), "  "))
		b.WriteString("\n")
		b.WriteString("  " + helpStyle.Render("o: open in Apple Books • r: reveal in Finder • enter / n: convert another • q: quit"))

	case stateError:
		errContent := fmt.Sprintf(
			"%s\n\n%s",
			errorStyle.Render("✗ Conversion Failed"),
			m.err.Error(),
		)
		b.WriteString("  " + indentMultiline(errorBoxStyle.Render(errContent), "  "))
		b.WriteString("\n")
		b.WriteString("  " + helpStyle.Render("enter / r: edit input & retry • q / esc: quit"))
	}

	b.WriteString("\n")
	return b.String()
}

func indentMultiline(s string, indent string) string {
	lines := strings.Split(s, "\n")
	return strings.Join(lines, "\n"+indent)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
