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
	if got, want := FindingFilename(f), "critical_"+strings.Repeat("é", 54)+"__7.md"; got != want {
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
		{db.DBFinding{ID: 7, Severity: "high", Name: "short."}, "high_short.__7.md"},
		{db.DBFinding{ID: 7, ExportCaseID: 42, Severity: "high", Name: "short."}, "case_42_high_short.md"},
		{db.DBFinding{ID: 7}, "info_未分类__7.md"},
		{db.DBFinding{ID: 7, Severity: "../../high", Name: "../a\\b\x00\r\n"}, "high_.._a_b___7.md"},
	} {
		if got := FindingFilename(&test.finding); got != test.want {
			t.Errorf("filename = %q, want %q", got, test.want)
		}
	}
}

func TestFindingsCSVTextSafetyAndRoundTrip(t *testing.T) {
	for _, value := range []string{"=1+1", "+1+1", "-2+3", "@sample", "  =1", "\u2003\x00=1", "\u202e=1", "＝1", "＋1", "－1", "＠sample", "  ＝1", "\x00text", "\ttext", "\rtext", "\ntext", "plain,\"quoted\"\nsecond line", "普通文本", "-42", "+3.14", "-1e3", "  ordinary", ""} {
		f := &db.DBFinding{ID: 7, Name: value, VulnClass: value, Severity: value, Status: value, TaskDescription: value, Summary: value}
		data := FindingsCSV([]*db.DBFinding{f})
		if !strings.HasPrefix(string(data), "\xef\xbb\xbf") {
			t.Fatal("missing BOM")
		}
		rows, err := csv.NewReader(strings.NewReader(string(data[3:]))).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || len(rows[1]) != 10 {
			t.Fatalf("unexpected CSV dimensions: %v", rows)
		}
		row := rows[1]
		if row[0] != "7" || row[8] != "0" {
			t.Fatal("numeric columns changed")
		}
		for _, col := range []int{1, 2, 3, 4, 5, 7} {
			input := value
			switch col {
			case 1:
				input = findingTitle(f)
			case 3:
				input = nz(value, "info")
			case 4:
				input = nz(value, "pending")
			case 7:
				input = strings.TrimSpace(value)
			}
			want := input
			trimmed := strings.TrimLeft(input, " \u2003\x00\u202e")
			if strings.HasPrefix(trimmed, "=") || strings.HasPrefix(trimmed, "@") || strings.HasPrefix(trimmed, "＝") || strings.HasPrefix(trimmed, "＋") || strings.HasPrefix(trimmed, "－") || strings.HasPrefix(trimmed, "＠") || input == "+1+1" || input == "-2+3" || strings.HasPrefix(input, "\x00") || strings.HasPrefix(input, "\t") || strings.HasPrefix(input, "\r") || strings.HasPrefix(input, "\n") {
				want = "'" + input
			}
			// encoding/csv normalizes embedded CRLF, and these fixtures contain no CRLF.
			if row[col] != want {
				t.Errorf("value %q col %d = %q, want %q", value, col, row[col], want)
			}
		}
		if f.TaskDescription != value {
			t.Fatal("export mutated input")
		}
	}
}

func TestFindingFilenameRetainsIdentityAndInternationalMarks(t *testing.T) {
	if got := FindingFilename(&db.DBFinding{ID: 9, Name: "safe\u202etitle\u2066", Severity: "high"}); got != "high_safetitle__9.md" {
		t.Fatalf("BiDi controls retained or visible title changed: %q", got)
	}
	longName := strings.Repeat("中", 100)
	a := FindingFilename(&db.DBFinding{ID: 7, Name: longName})
	b := FindingFilename(&db.DBFinding{ID: 8, Name: longName})
	c := FindingFilename(&db.DBFinding{ID: 7, ExportCaseID: 8, Name: longName})
	if a == b || a == c || b == c {
		t.Fatalf("distinct original/case identities collide: %q, %q, %q", a, b, c)
	}
	for _, name := range []string{"ثَغْرَة", "Gu\u0308venlik", "हिन्दी", "日本語", "e\u0301"} {
		got := FindingFilename(&db.DBFinding{ID: 42, Name: name, Severity: "high"})
		if got != "high_"+name+"__42.md" {
			t.Errorf("international title changed: %q", got)
		}
	}
	for _, caseID := range []int64{0, 42} {
		for _, id := range []int64{7, 8} {
			f := &db.DBFinding{ID: id, ExportCaseID: caseID, Severity: strings.Repeat("中", 70), Name: "safe\u202etitle/\\\x00"}
			got := FindingFilename(f)
			wantSuffix := "__7.md"
			if id == 8 {
				wantSuffix = "__8.md"
			}
			if caseID > 0 {
				wantSuffix = ".md"
				if !strings.HasPrefix(got, "case_42_") {
					t.Fatalf("case identity lost: %q", got)
				}
			}
			if len(got) > 123 || !utf8.ValidString(got) || !strings.HasSuffix(got, wantSuffix) || strings.ContainsAny(got, "/\\\x00\u202e") {
				t.Fatalf("unsafe or ambiguous filename: %q", got)
			}
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
