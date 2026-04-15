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
	Old  string `json:"old"`
	New  string `json:"new"`
	Line int    `json:"line"` // 1-based line number in the original file
}

// diffLines calls fn(lineNumber, old, new) for every changed line position.
// lineNumber is 1-based and refers to the original file.
// Uses an LCS-based diff so insertions (e.g. comment lines added above FROM)
// are reported correctly without shifting subsequent unchanged lines.
func diffLines(original, updated string, fn func(line int, oldLine, newLine string)) {
	a := strings.Split(original, "\n")
	b := strings.Split(updated, "\n")
	lcs := buildLCS(a, b)
	walkEdits(a, b, lcs, fn)
}

// buildLCS builds the longest-common-subsequence table for two string slices.
func buildLCS(a, b []string) [][]int {
	m, n := len(a), len(b)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	return dp
}

// walkEdits traverses the LCS edit script and calls fn for each changed position.
// Deletions and insertions at the same position are paired as substitutions.
func walkEdits(a, b []string, dp [][]int, fn func(line int, oldLine, newLine string)) {
	i, j := 0, 0
	m, n := len(a), len(b)
	for i < m || j < n {
		if i < m && j < n && a[i] == b[j] {
			i++
			j++
			continue
		}
		dels, ins := collectChunk(a, b, dp, &i, &j, m, n)
		emitChunk(dels, ins, i, fn)
	}
}

// collectChunk advances i and j past one block of consecutive deletions then
// insertions, returning the collected lines.
func collectChunk(a, b []string, dp [][]int, i, j *int, m, n int) (dels, ins []string) {
	for *i < m && (*j >= n || dp[*i+1][*j] >= dp[*i][*j+1]) {
		dels = append(dels, a[*i])
		*i++
	}
	for *j < n && (*i >= m || dp[*i][*j+1] > dp[*i+1][*j]) {
		ins = append(ins, b[*j])
		*j++
	}
	return dels, ins
}

// emitChunk pairs deletions with insertions as substitutions and emits leftovers.
func emitChunk(dels, ins []string, iAfter int, fn func(int, string, string)) {
	base := iAfter - len(dels)
	k := 0
	for ; k < len(dels) && k < len(ins); k++ {
		fn(base+k+1, dels[k], ins[k])
	}
	for ; k < len(dels); k++ {
		fn(base+k+1, dels[k], "")
	}
	for ; k < len(ins); k++ {
		fn(iAfter+1, "", ins[k])
	}
}

// collectChanges diffs original vs updated and returns the hunks.
func collectChanges(path, original, updated string) FileChange {
	fc := FileChange{Path: path}
	diffLines(original, updated, func(line int, o, u string) {
		fc.Changes = append(fc.Changes, Hunk{Old: o, New: u, Line: line})
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
	sarifVersion  = "2.1.0"
	sarifSchema   = "https://schemastore.azurewebsites.net/schemas/json/sarif-2.1.0.json"
	sarifRuleID   = "floating-ref"
	sarifToolName = "shapin"
)

// sarifText wraps a string in the SARIF message/shortDescription shape.
type sarifText struct {
	Text string `json:"text"`
}

// renderSARIF writes changes in SARIF 2.1.0 format for GitHub Code Scanning.
func renderSARIF(out io.Writer, changes []FileChange, version string) error {
	type artifactLocation struct {
		URI string `json:"uri"`
	}
	type region struct {
		StartLine int `json:"startLine"`
	}
	type physicalLocation struct {
		ArtifactLocation artifactLocation `json:"artifactLocation"`
		Region           region           `json:"region"`
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
		Tool struct {
			Driver driver `json:"driver"`
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
			loc := location{PhysicalLocation: physicalLocation{
				ArtifactLocation: artifactLocation{URI: fc.Path},
				Region:           region{StartLine: h.Line},
			}}
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
			Tool: struct {
				Driver driver `json:"driver"`
			}{Driver: driver{
				Name:    sarifToolName,
				Version: version,
				Rules:   []rule{{ID: sarifRuleID, ShortDescription: sarifText{"Floating tag or ref pinned to immutable SHA"}}},
			}},
			Results: results,
		}},
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}
