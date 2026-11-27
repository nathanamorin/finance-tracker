package xlsxutil

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// buildXLSX writes a minimal workbook in memory (synthetic test data).
func buildXLSX(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const ns = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`

func basicParts() map[string]string {
	return map[string]string{
		"xl/workbook.xml": `<workbook ` + ns + `><sheets>
			<sheet name="Notes" sheetId="2" r:id="rId2"/>
			<sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
			<Relationship Id="rId1" Target="worksheets/sheet1.xml"/>
			<Relationship Id="rId2" Target="/xl/worksheets/sheet2.xml"/></Relationships>`,
		"xl/sharedStrings.xml": `<sst ` + ns + `><si><t>Name</t></si><si><r><t>Ri</t></r><r><t>ch</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet ` + ns + `><sheetData>
			<row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1" t="inlineStr"><is><t>Inline</t></is></c></row>
			<row r="3"><c r="A3" t="s"><v>1</v></c><c r="B3"><v>12.418</v></c><c r="AB3"><v>45334</v></c></row>
		</sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml": `<worksheet ` + ns + `><sheetData><row r="1"><c r="A1" t="str"><v>FAKE DATA</v></c></row></sheetData></worksheet>`,
	}
}

func TestRead(t *testing.T) {
	sheets, err := Read(buildXLSX(t, basicParts()))
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) != 2 || sheets[0].Name != "Notes" || sheets[1].Name != "Data" {
		t.Fatalf("sheets: %+v", sheets)
	}
	if sheets[0].Rows[0][0] != "FAKE DATA" {
		t.Fatalf("absolute-target sheet: %+v", sheets[0].Rows)
	}
	rows := sheets[1].Rows
	if len(rows) != 3 || len(rows[1]) != 0 {
		t.Fatalf("row 2 should be preserved empty: %q", rows)
	}
	if got := strings.Join(rows[0], "|"); got != "Name||Inline" {
		t.Fatalf("row 1 = %q", got)
	}
	if rows[2][0] != "Rich" || rows[2][1] != "12.418" || len(rows[2]) != 28 || rows[2][27] != "45334" {
		t.Fatalf("row 3 = %q", rows[2])
	}
}

func TestSerialDate(t *testing.T) {
	if d, err := SerialDate("45334"); err != nil || d != "2024-02-12" {
		t.Fatalf("got %s %v", d, err)
	}
	if _, err := SerialDate("12-FEB-2024"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRejectsBadInput(t *testing.T) {
	if _, err := Read([]byte("Plan Name,Date Range\n")); err == nil {
		t.Fatal("CSV must not parse as xlsx")
	}
	parts := basicParts()
	parts["xl/sharedStrings.xml"] = `<sst ` + ns + `><si><t>only</t></si></sst>`
	parts["xl/worksheets/sheet1.xml"] = `<worksheet ` + ns + `><sheetData><row r="1"><c r="A1" t="s"><v>7</v></c></row></sheetData></worksheet>`
	if _, err := Read(buildXLSX(t, parts)); err == nil {
		t.Fatal("expected out-of-range shared string error")
	}
}

func TestDecompressionBudget(t *testing.T) {
	parts := basicParts()
	// Highly compressible: tiny zipped, larger than the budget unzipped.
	parts["xl/sharedStrings.xml"] = `<sst ` + ns + `><si><t>` + strings.Repeat("A", MaxDecompressed+10) + `</t></si></sst>`
	if _, err := Read(buildXLSX(t, parts)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected size error, got %v", err)
	}
}
