// Package xlsxutil is a minimal, dependency-free reader for .xlsx
// workbooks: it returns every sheet's cells as strings, which is all the
// import parsers need. Numbers come back as their stored text (e.g.
// "12.418"), so callers parse them with csvutil.ParseNumber; dates stored
// as Excel serial numbers can be converted with SerialDate.
//
// Uploaded files are untrusted, so reads are bounded: the total
// decompressed size of the parts read is capped (zip-bomb protection),
// and only the workbook, relationships, shared strings and worksheets are
// opened.
package xlsxutil

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
)

// MaxDecompressed caps the total decompressed bytes read from one workbook.
const MaxDecompressed = 64 << 20

// Sheet is one worksheet's cell text, row-major. Rows are padded so a
// cell's column index matches its spreadsheet column (A=0); empty rows in
// the sheet are preserved as empty slices so row numbers stay meaningful.
type Sheet struct {
	Name string
	Rows [][]string
}

// IsXLSX reports whether content looks like a zip archive (xlsx container).
func IsXLSX(content []byte) bool {
	return bytes.HasPrefix(content, []byte("PK\x03\x04"))
}

type reader struct {
	files  map[string]*zip.File
	budget int64
}

func (r *reader) read(name string) ([]byte, error) {
	f, ok := r.files[name]
	if !ok {
		return nil, fmt.Errorf("xlsx: missing part %s", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, r.budget+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > r.budget {
		return nil, errors.New("xlsx: workbook too large when decompressed")
	}
	r.budget -= int64(len(data))
	return data, nil
}

// Read parses every worksheet in the workbook.
func Read(content []byte) ([]Sheet, error) {
	if !IsXLSX(content) {
		return nil, errors.New("xlsx: not a zip/xlsx file")
	}
	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, fmt.Errorf("xlsx: %w", err)
	}
	r := &reader{files: map[string]*zip.File{}, budget: MaxDecompressed}
	for _, f := range zr.File {
		r.files[strings.TrimPrefix(f.Name, "/")] = f
	}

	// Workbook: sheet names and their relationship IDs, in tab order.
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := r.unmarshal("xl/workbook.xml", &wb); err != nil {
		return nil, err
	}
	var rels struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := r.unmarshal("xl/_rels/workbook.xml.rels", &rels); err != nil {
		return nil, err
	}
	targets := map[string]string{}
	for _, rel := range rels.Rels {
		t := rel.Target
		if strings.HasPrefix(t, "/") {
			t = strings.TrimPrefix(t, "/")
		} else {
			t = path.Join("xl", t)
		}
		targets[rel.ID] = t
	}

	var shared []string
	if _, ok := r.files["xl/sharedStrings.xml"]; ok {
		var sst struct {
			SI []richText `xml:"si"`
		}
		if err := r.unmarshal("xl/sharedStrings.xml", &sst); err != nil {
			return nil, err
		}
		for _, si := range sst.SI {
			shared = append(shared, si.String())
		}
	}

	var sheets []Sheet
	for _, s := range wb.Sheets {
		target, ok := targets[s.RID]
		if !ok {
			return nil, fmt.Errorf("xlsx: sheet %q has no relationship target", s.Name)
		}
		rows, err := r.sheet(target, shared)
		if err != nil {
			return nil, fmt.Errorf("xlsx: sheet %q: %w", s.Name, err)
		}
		sheets = append(sheets, Sheet{Name: s.Name, Rows: rows})
	}
	return sheets, nil
}

func (r *reader) unmarshal(name string, v any) error {
	data, err := r.read(name)
	if err != nil {
		return err
	}
	if err := xml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("xlsx: %s: %w", name, err)
	}
	return nil
}

// richText is a shared/inline string: plain <t> or rich-text runs <r><t>.
type richText struct {
	T    *string `xml:"t"`
	Runs []struct {
		T string `xml:"t"`
	} `xml:"r"`
}

func (rt richText) String() string {
	if rt.T != nil {
		return *rt.T
	}
	var b strings.Builder
	for _, r := range rt.Runs {
		b.WriteString(r.T)
	}
	return b.String()
}

func (r *reader) sheet(name string, shared []string) ([][]string, error) {
	var ws struct {
		Rows []struct {
			R     int `xml:"r,attr"`
			Cells []struct {
				Ref    string    `xml:"r,attr"`
				Type   string    `xml:"t,attr"`
				Value  *string   `xml:"v"`
				Inline *richText `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := r.unmarshal(name, &ws); err != nil {
		return nil, err
	}
	var rows [][]string
	for i, row := range ws.Rows {
		rowNum := row.R
		if rowNum <= 0 {
			rowNum = len(rows) + 1
		}
		if rowNum > 1_000_000 || rowNum < len(rows) {
			return nil, fmt.Errorf("bad row number %d at row element %d", row.R, i)
		}
		for len(rows) < rowNum-1 {
			rows = append(rows, nil)
		}
		var cells []string
		for j, c := range row.Cells {
			col := j
			if c.Ref != "" {
				var err error
				if col, err = columnIndex(c.Ref); err != nil {
					return nil, err
				}
			}
			if col > 16384 {
				return nil, fmt.Errorf("cell %s out of range", c.Ref)
			}
			var v string
			switch c.Type {
			case "s":
				if c.Value != nil {
					idx, err := strconv.Atoi(strings.TrimSpace(*c.Value))
					if err != nil || idx < 0 || idx >= len(shared) {
						return nil, fmt.Errorf("cell %s: bad shared string index", c.Ref)
					}
					v = shared[idx]
				}
			case "inlineStr":
				if c.Inline != nil {
					v = c.Inline.String()
				}
			default: // n, str, b, e, d
				if c.Value != nil {
					v = *c.Value
				}
			}
			for len(cells) < col {
				cells = append(cells, "")
			}
			if col < len(cells) {
				cells[col] = v
			} else {
				cells = append(cells, v)
			}
		}
		rows = append(rows, cells)
	}
	return rows, nil
}

// columnIndex converts a cell reference like "AB12" to a 0-based column.
func columnIndex(ref string) (int, error) {
	col := 0
	n := 0
	for _, ch := range ref {
		if ch >= 'A' && ch <= 'Z' {
			col = col*26 + int(ch-'A'+1)
			n++
		} else if ch >= 'a' && ch <= 'z' {
			col = col*26 + int(ch-'a'+1)
			n++
		} else {
			break
		}
	}
	if n == 0 || n > 3 {
		return 0, fmt.Errorf("bad cell reference %q", ref)
	}
	return col - 1, nil
}

// SerialDate converts an Excel serial date number ("45334") to a Date.
// Only the 1900 date system is supported (the default everywhere but very
// old Mac workbooks).
func SerialDate(s string) (ingest.Date, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 1 || f > 2958465 {
		return "", fmt.Errorf("not an Excel serial date: %q", s)
	}
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	return ingest.NewDate(base.AddDate(0, 0, int(math.Floor(f)))), nil
}
