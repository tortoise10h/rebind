package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"rebind/internal/converter"
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
		if err := runInteractive(initialURL, defaultOutDir); err != nil {
			fmt.Fprintf(os.Stderr, "Error running rebind: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "Error: headless conversion requires a URL or PDF file path argument\n")
		flag.Usage()
		os.Exit(1)
	}
	targetInput := flag.Arg(0)

	cfg := headlessConfig{
		targetInput:   targetInput,
		outTarget:     outTarget,
		defaultOutDir: defaultOutDir,
		profileStr:    profileStr,
		firstPage:     firstPage,
		lastPage:      lastPage,
	}

	if err := runHeadless(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "\n%v\n", err)
		os.Exit(1)
	}
}
