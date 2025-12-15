package codebase

import "fmt"

// Analyzer previously scanned the local filesystem for code.
// Code scanning has been disabled.
type Analyzer struct {
	basePath string
}

func NewAnalyzer(basePath string) *Analyzer {
	return &Analyzer{basePath: basePath}
}

func (a *Analyzer) GetRelevantContext(question string) (string, error) {
	return "", fmt.Errorf("code scanning disabled")
}
