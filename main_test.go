package main

import (
	"testing"
)

func TestParsePageRange(t *testing.T) {
	tests := []struct {
		input     string
		wantFirst int
		wantLast  int
		wantErr   bool
	}{
		{"", 0, 0, false},
		{"15", 15, 15, false},
		{"15-16", 15, 16, false},
		{"1-10", 1, 10, false},
		{"0", 0, 0, true},
		{"-5", 0, 5, false},
		{"15-", 15, 0, false},
		{"-16", 0, 16, false},
		{"abc", 0, 0, true},
		{"1-2-3", 0, 0, true},
	}

	for _, tt := range tests {
		first, last, err := parsePageRange(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parsePageRange(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if first != tt.wantFirst || last != tt.wantLast {
			t.Errorf("parsePageRange(%q) = (%d, %d), want (%d, %d)", tt.input, first, last, tt.wantFirst, tt.wantLast)
		}
	}
}

func TestRunHeadless_MissingInput(t *testing.T) {
	err := runHeadless(headlessConfig{})
	if err == nil {
		t.Errorf("expected error when targetInput is empty, got nil")
	}
}
