package main

import (
	tea "github.com/charmbracelet/bubbletea"

	"rebind/internal/tui"
)

func runInteractive(initialURL, defaultOutDir string) error {
	p := tea.NewProgram(tui.NewModel(initialURL, defaultOutDir))
	_, err := p.Run()
	return err
}
