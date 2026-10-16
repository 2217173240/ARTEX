package report

import (
	"encoding/csv"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

func TestExportSeverityOrder(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	severities := []string{"info", "Critical", "中危", "unassessed", "", "low", "medium", "high", "critical"}
	findings := make([]*db.DBFinding, 0, len(severities))
	nodes := make([]*db.Node, 0, len(severities))
	for i, severity := range severities {
		name := "finding-" + string(rune('a'+i))
		findings = append(findings, &db.DBFinding{ID: int64(i + 1), Name: name, Severity: severity, CreatedAt: now})
		payload, err := json.Marshal(map[string]string{"name": name, "severity": severity})
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, &db.Node{Payload: payload})
	}
	wantNames := []string{"finding-i", "finding-h", "finding-g", "finding-f", "finding-a", "finding-b", "finding-c", "finding-d", "finding-e"}
	for name, output := range map[string]string{
		"findings markdown": FindingsMarkdown(findings, now),
		"graph markdown":    Markdown(Input{Findings: nodes, GeneratedAt: now}),
	} {
		t.Run(name, func(t *testing.T) {
			previous := -1
			for _, findingName := range wantNames {
				index := strings.Index(output, findingName)
				if index <= previous {
					t.Fatalf("severity order does not match %v:\n%s", wantNames, output)
				}
				previous = index
			}
		})
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(FindingsCSV(findings)), "\xef\xbb\xbf"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, row := range rows[1:] {
		names = append(names, row[1])
	}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("CSV names = %v, want %v", names, wantNames)
	}
	for i, finding := range findings {
		if finding.Severity != severities[i] {
			t.Fatal("export changed caller's finding order")
		}
	}
}

func TestExportSeverityKeepsNewestFirstWithinRank(t *testing.T) {
	now := time.Now()
	findings := []*db.DBFinding{
		{Name: "old-unknown", Severity: "info", CreatedAt: now.Add(-time.Hour)},
		{Name: "old-high", Severity: "high", CreatedAt: now.Add(-time.Hour)},
		{Name: "new-unknown", Severity: "unassessed", CreatedAt: now},
		{Name: "new-high", Severity: "high", CreatedAt: now},
	}
	sortFindingsForExport(findings)
	var names []string
	for _, f := range findings {
		names = append(names, f.Name)
	}
	want := []string{"new-high", "old-high", "new-unknown", "old-unknown"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func TestFindingFilenameUTF8ByteLimit(t *testing.T) {
	for _, title := range []string{strings.Repeat("中", 80), strings.Repeat("é", 80), strings.Repeat("𐐀", 80), strings.Repeat("a", 200)} {
		for _, caseID := range []int64{0, 42} {
			f := &db.DBFinding{ID: 7, ExportCaseID: caseID, Severity: "critical", Name: title}
			got := FindingFilename(f)
			if !utf8.ValidString(got) || len(got) > 123 || !strings.HasSuffix(got, ".md") {
				t.Errorf("filename = %q (valid UTF-8 %t, %d bytes)", got, utf8.ValidString(got), len(got))
			}
		}
	}
	// Keep the byte budget, rather than increasing it to 120 Unicode characters.
	f := &db.DBFinding{ID: 7, Severity: "critical", Name: strings.Repeat("é", 80)}
	if got, want := FindingFilename(f), "critical_"+strings.Repeat("é", 55)+".md"; got != want {
		t.Errorf("filename = %q, want maximal whole-rune prefix %q", got, want)
	}
}

func TestFindingFilenameSanitizationAndShortNames(t *testing.T) {
	for _, test := range []struct {
		finding db.DBFinding
		want    string
	}{
		{db.DBFinding{ID: 7, Severity: "high", Name: "SQL注入"}, "high_SQL注入__7.md"},
		{db.DBFinding{ID: 7, ExportCaseID: 42, Severity: "high", Name: "SQL注入"}, "case_42_high_SQL注入.md"},
		{db.DBFinding{ID: 7}, "info_未分类__7.md"},
		{db.DBFinding{ID: 7, Severity: "../../high", Name: "../a\\b\x00\r\n"}, "high_.._a_b___7.md"},
	} {
		if got := FindingFilename(&test.finding); got != test.want {
			t.Errorf("filename = %q, want %q", got, test.want)
		}
	}
}

func TestEvidenceStaysInsideCodeFence(t *testing.T) {
	for _, evidence := range []string{
		"ordinary evidence",
		"response\n```\n# injected heading\n![image](https://example.invalid/image)\n```\nend",
		"``````\n<script>untrusted</script>\n``````\n~~~~~~~",
		"before `` middle ` after\n   `````   \n# heading\n",
	} {
		payload, err := json.Marshal(map[string]any{"name": "sample", "evidence": map[string]string{"poc": evidence}})
		if err != nil {
			t.Fatal(err)
		}
		for name, output := range map[string]string{
			"findings markdown": FindingsMarkdown([]*db.DBFinding{{Evidence: evidence}}, time.Time{}),
			"single markdown":   SingleFindingMarkdown(&db.DBFinding{Evidence: evidence}, time.Time{}),
			"graph markdown":    Markdown(Input{Findings: []*db.Node{{Payload: payload}}}),
		} {
			t.Run(name+"/"+evidence, func(t *testing.T) {
				lines := strings.Split(output, "\n")
				for i, line := range lines {
					if len(line) >= 3 && strings.Trim(line, "`") == "" {
						body := evidence
						if name != "graph markdown" {
							body = strings.TrimSpace(body)
						}
						if strings.Contains(body, line) {
							t.Fatalf("opening fence %q occurs inside evidence:\n%s", line, output)
						}
						want := line + "\n" + body + "\n" + line
						if !strings.Contains(strings.Join(lines[i:], "\n"), want) {
							t.Fatalf("evidence or closing fence changed:\n%s", output)
						}
						return
					}
				}
				t.Fatal("missing evidence fence")
			})
		}
	}
}
