package scanner

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// FileChange records what changed in a single file.
type FileChange struct {
	Path    string `json:"path"`
	Changes []Hunk `json:"changes"`
}

// Hunk is a single line-level change (old → new).
type Hunk struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// diffLines calls fn(old, new) for every line position that differs between a and b.
func diffLines(original, updated string, fn func(oldLine, newLine string)) {
	originalLines := strings.Split(original, "\n")
	updatedLines := strings.Split(updated, "\n")
	lineCount := max(len(originalLines), len(updatedLines))
	for i := range lineCount {
		var oldLine, newLine string
		if i < len(originalLines) {
			oldLine = originalLines[i]
		}
		if i < len(updatedLines) {
			newLine = updatedLines[i]
		}
		if oldLine != newLine {
			fn(oldLine, newLine)
		}
	}
}

// collectChanges diffs original vs updated and returns the hunks.
func collectChanges(path, original, updated string) FileChange {
	fc := FileChange{Path: path}
	diffLines(original, updated, func(o, u string) {
		fc.Changes = append(fc.Changes, Hunk{Old: o, New: u})
	})
	return fc
}

// renderJSON writes all changes as a JSON array.
func renderJSON(out io.Writer, changes []FileChange) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(changes)
}

const (
	sarifVersion   = "2.1.0"
	sarifSchema    = "https://schemastore.azurewebsites.net/schemas/json/sarif-2.1.0.json"
	sarifRuleID    = "floating-ref"
	sarifToolName  = "digestify-my-ci"
	sarifToolVer   = "0.0.0"
)

// sarifText wraps a string in the SARIF message/shortDescription shape.
type sarifText struct {
	Text string `json:"text"`
}

// renderSARIF writes changes in SARIF 2.1.0 format for GitHub Code Scanning.
func renderSARIF(out io.Writer, changes []FileChange) error {
	type artifactLocation struct {
		URI string `json:"uri"`
	}
	type physicalLocation struct {
		ArtifactLocation artifactLocation `json:"artifactLocation"`
	}
	type location struct {
		PhysicalLocation physicalLocation `json:"physicalLocation"`
	}
	type result struct {
		RuleID    string     `json:"ruleId"`
		Message   sarifText  `json:"message"`
		Locations []location `json:"locations"`
	}
	type rule struct {
		ID               string    `json:"id"`
		ShortDescription sarifText `json:"shortDescription"`
	}
	type driver struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Rules   []rule `json:"rules"`
	}
	type run struct {
		Tool    struct{ Driver driver `json:"driver"` } `json:"tool"`
		Results []result                                `json:"results"`
	}
	type sarif struct {
		Version string `json:"version"`
		Schema  string `json:"$schema"`
		Runs    []run  `json:"runs"`
	}

	var results []result
	for _, fc := range changes {
		loc := location{PhysicalLocation: physicalLocation{ArtifactLocation: artifactLocation{URI: fc.Path}}}
		for _, h := range fc.Changes {
			results = append(results, result{
				RuleID:    sarifRuleID,
				Message:   sarifText{fmt.Sprintf("Floating ref pinned: %q → %q", strings.TrimSpace(h.Old), strings.TrimSpace(h.New))},
				Locations: []location{loc},
			})
		}
	}

	s := sarif{
		Version: sarifVersion,
		Schema:  sarifSchema,
		Runs: []run{{
			Tool: struct{ Driver driver `json:"driver"` }{Driver: driver{
				Name:    sarifToolName,
				Version: sarifToolVer,
				Rules:   []rule{{ID: sarifRuleID, ShortDescription: sarifText{"Floating tag or ref pinned to immutable SHA"}}},
			}},
			Results: results,
		}},
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}
