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
	state         state
	input         textinput.Model
	spinner       spinner.Model
	outputDir     string
	profile       converter.BookProfile
	isPDF         bool
	currentStep   converter.Step
	statusMessage string
	result        *converter.Result
	err           error
	actionStatus  string
	progressCh    chan converter.ProgressEvent
}

// NewModel initializes a new TUI model.
func NewModel(initialURL string, outputDir string) Model {
	ti := textinput.New()
	ti.Placeholder = "https://example.com/post or ~/Books/novel.pdf"
	ti.Focus()
	ti.CharLimit = 2048
	ti.Width = 64
	ti.Prompt = "Input > "
	ti.PromptStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#A29BFE"))
	if initialURL != "" {
		ti.SetValue(initialURL)
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#A29BFE"))

	return Model{
		state:     stateInput,
		input:     ti,
		spinner:   sp,
		outputDir: outputDir,
		profile:   converter.ProfileAuto,
	}
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

func (m Model) Init() tea.Cmd {
	return textinput.Blink
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
				m.resetToInput(m.state == stateDone)
				return m, textinput.Blink
			}
			return m, tea.Quit
		}

		switch m.state {
		case stateInput:
			switch msg.String() {
			case "enter":
				raw := strings.TrimSpace(m.input.Value())
				if raw == "" {
					return m, nil
				}
				return m.startConversion(raw)
			case "tab":
				if isPDFInput(m.input.Value()) {
					switch m.profile {
					case converter.ProfileAuto:
						m.profile = converter.ProfileFiction
					case converter.ProfileFiction:
						m.profile = converter.ProfileTechnical
					default:
						m.profile = converter.ProfileAuto
					}
					return m, nil
				}
			case "shift+tab":
				if isPDFInput(m.input.Value()) {
					switch m.profile {
					case converter.ProfileAuto:
						m.profile = converter.ProfileTechnical
					case converter.ProfileTechnical:
						m.profile = converter.ProfileFiction
					default:
						m.profile = converter.ProfileAuto
					}
					return m, nil
				}
			}

		case stateDone:
			switch msg.String() {
			case "q":
				return m, tea.Quit
			case "enter", "n":
				m.resetToInput(true)
				return m, textinput.Blink
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
				m.resetToInput(false)
				return m, textinput.Blink
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
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m *Model) resetToInput(clearValue bool) {
	m.state = stateInput
	m.err = nil
	m.result = nil
	m.actionStatus = ""
	m.currentStep = 0
	m.statusMessage = ""
	if clearValue {
		m.input.SetValue("")
	}
	m.input.Focus()
}

func (m Model) startConversion(rawInput string) (tea.Model, tea.Cmd) {
	m.state = stateConverting
	m.err = nil
	m.result = nil
	m.actionStatus = ""
	m.currentStep = converter.StepFetching
	m.statusMessage = "Starting conversion..."
	m.isPDF = isPDFInput(rawInput)
	m.input.Blur()

	ch := make(chan converter.ProgressEvent, 16)
	m.progressCh = ch
	outDir := m.outputDir
	profile := m.profile

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

func (m Model) View() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("  " + titleStyle.Render("rebind") + "  " + subtitleStyle.Render("Web Article & PDF to EPUB Converter (Apple Books / Kindle)"))
	b.WriteString("\n")
	b.WriteString("  " + dimStyle.Render("Output directory: "+m.outputDir))
	b.WriteString("\n\n")

	switch m.state {
	case stateInput:
		selector := ""
		if isPDFInput(m.input.Value()) {
			selector = renderProfileSelector(m.profile)
		}
		content := fmt.Sprintf(
			"%s\n\n%s%s",
			labelStyle.Render("Paste a web URL or local PDF path (drag & drop supported):"),
			m.input.View(),
			selector,
		)
		b.WriteString("  " + indentMultiline(boxStyle.Render(content), "  "))
		b.WriteString("\n")
		b.WriteString("  " + helpStyle.Render("enter: convert to epub • tab: switch profile • esc / ctrl+c: quit"))

	case stateConverting:
		var stepsView strings.Builder
		stepsView.WriteString(labelStyle.Render("Converting: ") + dimStyle.Render(truncate(m.input.Value(), 54)) + "\n\n")

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
		b.WriteString("  " + helpStyle.Render("enter / r: edit URL & retry • q / esc: quit"))
	}

	b.WriteString("\n")
	return b.String()
}

func renderProfileSelector(current converter.BookProfile) string {
	autoRadio := "○ Auto-Detect"
	fictionRadio := "○ Fiction"
	techRadio := "○ Technical"

	active := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#A29BFE"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C6C80"))

	switch current {
	case converter.ProfileFiction:
		fictionRadio = "[● Fiction]"
	case converter.ProfileTechnical:
		techRadio = "[● Technical]"
	default:
		autoRadio = "[● Auto-Detect]"
	}

	return fmt.Sprintf(
		"\n\n%s  %s   %s   %s     %s",
		dim.Render("Book Profile:"),
		active.Render(autoRadio),
		active.Render(fictionRadio),
		active.Render(techRadio),
		dim.Render("(tab: switch profile)"),
	)
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
