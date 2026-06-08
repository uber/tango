// Copyright (c) 2025 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package changeanalyzer classifies the complexity of changes between two git refs.
package changeanalyzer

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/uber/tango/core/git"
)

// ChangeComplexity represents the complexity level of changes between two refs.
type ChangeComplexity int

const (
	// UnknownComplexity means changes between two refs are not categorized.
	UnknownComplexity ChangeComplexity = iota
	// NoChange means there's no change between two refs.
	NoChange
	// RegularFilesModificationOnly means only regular files are modified.
	RegularFilesModificationOnly
	// ReparsePackagesNeeded means the change requires reparse packages.
	ReparsePackagesNeeded
	// FullCalculationRequired means the change is too complex and requires full calculation.
	FullCalculationRequired
)

// FileCategory represents the type of file.
type FileCategory int

const (
	// UnknownCategory means the file category is unknown.
	UnknownCategory FileCategory = iota
	// RegularFile means the file is not any of the below types.
	RegularFile
	// BuildFile means the file is a build file, i.e. BUILD or BUILD.bazel.
	BuildFile
	// ExtensionFile means the file is an extension file, i.e. XXX.bzl.
	ExtensionFile
	// CriticalFile means the file is a critical file that should trigger full calculation, i.e. WORKSPACE.
	CriticalFile
	// IgnoredFile means the file is ignored.
	IgnoredFile
)

// ChangedFileStatus is the type of change for a file.
type ChangedFileStatus string

const (
	// Added indicates a file was added.
	Added ChangedFileStatus = "A"
	// Deleted indicates a file was deleted.
	Deleted ChangedFileStatus = "D"
	// Modified indicates a file was modified.
	Modified ChangedFileStatus = "M"
	// Renamed indicates a file was renamed.
	Renamed ChangedFileStatus = "R"
)

// AnalyzeChangeRequest represents the request to analyze the change.
type AnalyzeChangeRequest struct {
	BaseRef, TargetRef string
}

// AnalyzeChangeResponse represents the response of the change analysis.
type AnalyzeChangeResponse struct {
	ChangeComplexity ChangeComplexity
	Changes          []git.DiffEntry
	Summary          map[FileCategory]map[ChangedFileStatus]int
}

// Analyzer represents the change analyzer.
type Analyzer interface {
	AnalyzeChange(ctx context.Context, request *AnalyzeChangeRequest) (*AnalyzeChangeResponse, error)
	GetFileCategory(path string) FileCategory
}

type analyzer struct {
	git                  git.Interface
	buildFilePatterns    []*regexp.Regexp
	criticalFilePatterns []*regexp.Regexp
	ignoredFilePatterns  []*regexp.Regexp
}

// Config holds the pattern configuration for the analyzer.
type Config struct {
	BuildFilePatterns    []string
	CriticalFilePatterns []string
	IgnoredFilePatterns  []string
}

// NewAnalyzer creates a new Analyzer.
func NewAnalyzer(g git.Interface, cfg Config) (Analyzer, error) {
	buildPatterns, err := compilePatterns(cfg.BuildFilePatterns)
	if err != nil {
		return nil, fmt.Errorf("compiling build file patterns: %w", err)
	}
	criticalPatterns, err := compilePatterns(cfg.CriticalFilePatterns)
	if err != nil {
		return nil, fmt.Errorf("compiling critical file patterns: %w", err)
	}
	ignoredPatterns, err := compilePatterns(cfg.IgnoredFilePatterns)
	if err != nil {
		return nil, fmt.Errorf("compiling ignored file patterns: %w", err)
	}
	return &analyzer{
		git:                  g,
		buildFilePatterns:    buildPatterns,
		criticalFilePatterns: criticalPatterns,
		ignoredFilePatterns:  ignoredPatterns,
	}, nil
}

func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", p, err)
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}

// AnalyzeChange analyzes the change and returns the complexity level.
func (a *analyzer) AnalyzeChange(ctx context.Context, request *AnalyzeChangeRequest) (*AnalyzeChangeResponse, error) {
	changes, err := a.git.DiffWithStatus(ctx, request.BaseRef, request.TargetRef)
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return nil, err
		}

		errFetch := a.git.Fetch(ctx, "origin", "main", "--no-tags")
		if errFetch != nil {
			return nil, errors.Join(err, fmt.Errorf("fetch origin/main: %w", errFetch))
		}
		changes, err = a.git.DiffWithStatus(ctx, request.BaseRef, request.TargetRef)
	}
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	response := &AnalyzeChangeResponse{
		ChangeComplexity: UnknownComplexity,
		Changes:          changes,
	}

	if len(changes) == 0 {
		response.ChangeComplexity = NoChange
		return response, nil
	}

	summary := map[FileCategory]map[ChangedFileStatus]int{}
	for _, change := range changes {
		fileType := a.GetFileCategory(change.Path)
		if _, ok := summary[fileType]; !ok {
			summary[fileType] = map[ChangedFileStatus]int{}
		}
		summary[fileType][ChangedFileStatus(change.Status)]++
	}

	response.Summary = summary

	if hasOnlyIgnoredFiles(summary) {
		response.ChangeComplexity = NoChange
		return response, nil
	}

	if hasCriticalFiles(summary) {
		response.ChangeComplexity = FullCalculationRequired
		return response, nil
	}

	if summary[RegularFile][Modified] == len(changes) {
		response.ChangeComplexity = RegularFilesModificationOnly
		return response, nil
	}

	if isReparsePackagesNeeded(summary) {
		response.ChangeComplexity = ReparsePackagesNeeded
		return response, nil
	}

	return response, nil
}

// GetFileCategory returns the category of the file.
func (a *analyzer) GetFileCategory(path string) FileCategory {
	switch {
	case matchPatterns(a.criticalFilePatterns, path):
		return CriticalFile
	case matchPatterns(a.ignoredFilePatterns, path):
		return IgnoredFile
	case matchPatterns(a.buildFilePatterns, path):
		return BuildFile
	}

	switch filepath.Ext(path) {
	case ".bzl":
		return ExtensionFile
	default:
		return RegularFile
	}
}

func matchPatterns(patternsRegex []*regexp.Regexp, path string) bool {
	for _, patternRx := range patternsRegex {
		if patternRx.MatchString(path) {
			return true
		}
	}
	return false
}

func hasOnlyIgnoredFiles(summary map[FileCategory]map[ChangedFileStatus]int) bool {
	_, ok := summary[IgnoredFile]
	return ok && len(summary) == 1
}

func hasCriticalFiles(summary map[FileCategory]map[ChangedFileStatus]int) bool {
	_, ok := summary[CriticalFile]
	return ok
}

func isReparsePackagesNeeded(summary map[FileCategory]map[ChangedFileStatus]int) bool {
	for k := range summary {
		if k != RegularFile && k != BuildFile && k != IgnoredFile {
			return false
		}
	}
	return true
}
