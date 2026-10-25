package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rebind/internal/converter"
)

type headlessConfig struct {
	targetInput   string
	outTarget     string
	defaultOutDir string
	profileStr    string
	firstPage     int
	lastPage      int
}

func runHeadless(cfg headlessConfig) error {
	if cfg.targetInput == "" {
		return errors.New("headless conversion requires a URL or PDF file path argument")
	}

	opts := converter.ConvertOptions{
		Profile:   converter.BookProfile(cfg.profileStr),
		FirstPage: cfg.firstPage,
		LastPage:  cfg.lastPage,
	}

	if cfg.outTarget != "" {
		if strings.HasSuffix(strings.ToLower(cfg.outTarget), ".epub") {
			opts.OutputPath = cfg.outTarget
			opts.OutputDir = filepath.Dir(cfg.outTarget)
		} else {
			opts.OutputDir = cfg.outTarget
		}
	} else {
		opts.OutputDir = cfg.defaultOutDir
	}

	onProgress := func(ev converter.ProgressEvent) {
		fmt.Fprintf(os.Stderr, "[Step %d/4] %s\n", ev.Step, ev.Message)
	}

	ctx := context.Background()
	result, err := converter.ConvertWithOptions(ctx, cfg.targetInput, opts, onProgress)
	if err != nil {
		return fmt.Errorf("conversion failed: %w", err)
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
	return nil
}
