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

// collectChanges diffs original vs updated and returns the hunks.
func collectChanges(path, original, updated string) FileChange {
	origLines := strings.Split(original, "\n")
	updLines := strings.Split(updated, "\n")
	fc := FileChange{Path: path}

	maxLen := max(len(origLines), len(updLines))
	for i := range maxLen {
		var o, u string
		if i < len(origLines) {
			o = origLines[i]
		}
		if i < len(updLines) {
			u = updLines[i]
		}
		if o != u {
			fc.Changes = append(fc.Changes, Hunk{Old: o, New: u})
		}
	}
	return fc
}

// renderJSON writes all changes as a JSON array.
func renderJSON(out io.Writer, changes []FileChange) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(changes)
}

// renderSARIF writes changes in SARIF 2.1.0 format for GitHub Code Scanning.
func renderSARIF(out io.Writer, changes []FileChange) error {
	type location struct {
		PhysicalLocation struct {
			ArtifactLocation struct {
				URI string `json:"uri"`
			} `json:"artifactLocation"`
		} `json:"physicalLocation"`
	}
	type result struct {
		RuleID  string     `json:"ruleId"`
		Message struct {
			Text string `json:"text"`
		} `json:"message"`
		Locations []location `json:"locations"`
	}
	type run struct {
		Tool struct {
			Driver struct {
				Name    string `json:"name"`
				Version string `json:"version"`
				Rules   []struct {
					ID               string `json:"id"`
					ShortDescription struct {
						Text string `json:"text"`
					} `json:"shortDescription"`
				} `json:"rules"`
			} `json:"driver"`
		} `json:"tool"`
		Results []result `json:"results"`
	}
	type sarif struct {
		Version string `json:"version"`
		Schema  string `json:"$schema"`
		Runs    []run  `json:"runs"`
	}

	var results []result
	for _, fc := range changes {
		for _, h := range fc.Changes {
			var loc location
			loc.PhysicalLocation.ArtifactLocation.URI = fc.Path
			results = append(results, result{
				RuleID: "floating-ref",
				Message: struct {
					Text string `json:"text"`
				}{
					Text: fmt.Sprintf("Floating ref pinned: %q → %q", strings.TrimSpace(h.Old), strings.TrimSpace(h.New)),
				},
				Locations: []location{loc},
			})
		}
	}

	s := sarif{
		Version: "2.1.0",
		Schema:  "https://schemastore.azurewebsites.net/schemas/json/sarif-2.1.0.json",
		Runs: []run{
			{
				Results: results,
			},
		},
	}
	s.Runs[0].Tool.Driver.Name = "digestify-my-ci"
	s.Runs[0].Tool.Driver.Version = "0.0.0"
	s.Runs[0].Tool.Driver.Rules = []struct {
		ID               string `json:"id"`
		ShortDescription struct {
			Text string `json:"text"`
		} `json:"shortDescription"`
	}{
		{
			ID: "floating-ref",
			ShortDescription: struct {
				Text string `json:"text"`
			}{Text: "Floating tag or ref pinned to immutable SHA"},
		},
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}
