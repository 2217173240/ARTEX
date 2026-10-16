package server

import (
	"archive/zip"
	"bytes"
	"io"
	"path"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

// Exercise archive serialization with only synthetic findings: no Manager,
// configuration, traffic bindings, or database connection is needed.
func TestFindingExportZipPreservesUnicodeFilename(t *testing.T) {
	finding := &db.DBFinding{
		ID: 7, ExportCaseID: 42, Severity: "critical",
		Name: strings.Repeat("中", 80), Evidence: "response\n```\n# untrusted heading\n```",
	}
	var out bytes.Buffer
	if err := writeFindingsEvidenceZip(&out, []*db.DBFinding{finding}, t.TempDir(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 1 {
		t.Fatalf("archive has %d entries, want one Markdown report", len(archive.File))
	}
	entry := archive.File[0]
	if entry.Name != report.FindingFilename(finding) || !utf8.ValidString(entry.Name) || entry.NonUTF8 {
		t.Fatalf("archive filename = %q, NonUTF8=%t", entry.Name, entry.NonUTF8)
	}
	if path.Base(entry.Name) != entry.Name || !strings.HasSuffix(entry.Name, ".md") || len(entry.Name) > 123 {
		t.Fatalf("invalid archive path %q", entry.Name)
	}
	r, err := entry.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "````\n"+finding.Evidence+"\n````") {
		t.Fatalf("archive evidence was not safely fenced:\n%s", body)
	}
}
