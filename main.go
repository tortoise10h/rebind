package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"rebind/internal/converter"
	"rebind/internal/tui"
)

func parsePageRange(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	parts := strings.Split(s, "-")
	if len(parts) == 1 {
		p, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || p <= 0 {
			return 0, 0, fmt.Errorf("invalid page number: %s", s)
		}
		return p, p, nil
	}
	if len(parts) == 2 {
		firstStr := strings.TrimSpace(parts[0])
		lastStr := strings.TrimSpace(parts[1])
		first, last := 0, 0
		var err error
		if firstStr != "" {
			first, err = strconv.Atoi(firstStr)
			if err != nil || first <= 0 {
				return 0, 0, fmt.Errorf("invalid start page: %s", firstStr)
			}
		}
		if lastStr != "" {
			last, err = strconv.Atoi(lastStr)
			if err != nil || last <= 0 {
				return 0, 0, fmt.Errorf("invalid end page: %s", lastStr)
			}
		}
		return first, last, nil
	}
	return 0, 0, fmt.Errorf("invalid page range format: %s (expected N or N-M)", s)
}

func main() {
	var (
		headless    bool
		output      string
		outputShort string
		profileStr  string
		pagesStr    string
	)

	flag.BoolVar(&headless, "headless", false, "Run headlessly without interactive TUI")
	flag.StringVar(&output, "output", "", "Output EPUB file path or output directory")
	flag.StringVar(&outputShort, "o", "", "Output EPUB file path or output directory (shorthand)")
	flag.StringVar(&profileStr, "profile", "auto", "Book profile for PDF conversion (auto, fiction, technical)")
	flag.StringVar(&pagesStr, "pages", "", "Page range for PDF conversion (e.g. 15-16 or 15)")

	flag.Parse()

	defaultOutDir, err := converter.DefaultOutputDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving default output directory: %v\n", err)
		os.Exit(1)
	}

	outTarget := output
	if outTarget == "" {
		outTarget = outputShort
	}

	firstPage, lastPage, err := parsePageRange(pagesStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing --pages: %v\n", err)
		os.Exit(1)
	}

	isExplicitHeadless := headless || outTarget != "" || pagesStr != ""

	if !isExplicitHeadless {
		var initialURL string
		if flag.NArg() > 0 {
			initialURL = flag.Arg(0)
		}
		p := tea.NewProgram(tui.NewModel(initialURL, defaultOutDir))
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error running rebind: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Headless mode
	if flag.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "Error: headless conversion requires a URL or PDF file path argument\n")
		flag.Usage()
		os.Exit(1)
	}
	targetInput := flag.Arg(0)

	opts := converter.ConvertOptions{
		Profile:   converter.BookProfile(profileStr),
		FirstPage: firstPage,
		LastPage:  lastPage,
	}

	if outTarget != "" {
		if strings.HasSuffix(strings.ToLower(outTarget), ".epub") {
			opts.OutputPath = outTarget
			opts.OutputDir = filepath.Dir(outTarget)
		} else {
			opts.OutputDir = outTarget
		}
	} else {
		opts.OutputDir = defaultOutDir
	}

	onProgress := func(ev converter.ProgressEvent) {
		fmt.Fprintf(os.Stderr, "[Step %d/4] %s\n", ev.Step, ev.Message)
	}

	ctx := context.Background()
	result, err := converter.ConvertWithOptions(ctx, targetInput, opts, onProgress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nConversion failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nSuccessfully converted %q in %v\nOutput: %s\nChapters: %d | Pages: %d | Images: %d/%d | Profile: %s\n",
		result.Title,
		result.Duration.Round(time.Millisecond),
		result.OutputPath,
		result.Chapters,
		result.PagesTotal,
		result.ImagesSaved,
		result.ImagesTotal,
		result.ProfileUsed,
	)
}
