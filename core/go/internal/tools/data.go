package tools

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"path"
	"sort"
	"strings"
)

var ErrData = errors.New("data_error")

const (
	maxDataRows    = 100_000
	maxDataColumns = 200
	sampleRows     = 10
)

// ColumnStats are exact statistics for a numeric column (decimal strings).
type ColumnStats struct {
	Count int    `json:"count"`
	Sum   string `json:"sum"`
	Min   string `json:"min"`
	Max   string `json:"max"`
	Mean  string `json:"mean"`
}

// DataSummary is what data.read returns.
type DataSummary struct {
	Path     string                 `json:"path"`
	Format   string                 `json:"format"`
	Columns  []string               `json:"columns"`
	RowCount int                    `json:"row_count"`
	Sample   [][]string             `json:"sample"`
	Numeric  map[string]ColumnStats `json:"numeric"`
	SHA256   string                 `json:"sha256"`
}

// ReadData summarizes a CSV or JSON (array of objects) workspace file with
// exact column statistics computed by the Core, not by a model.
func (w *Workspace) ReadData(taskID, rel string) (*DataSummary, error) {
	f, err := w.Read(taskID, rel)
	if err != nil {
		return nil, err
	}
	var cols []string
	var rows [][]string
	format := strings.TrimPrefix(strings.ToLower(path.Ext(f.Path)), ".")
	switch format {
	case "csv", "tsv":
		r := csv.NewReader(bytes.NewReader([]byte(f.Content)))
		if format == "tsv" {
			r.Comma = '\t'
		}
		r.FieldsPerRecord = -1
		header, err := r.Read()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrData, err)
		}
		cols = header
		for {
			rec, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrData, err)
			}
			rows = append(rows, rec)
			if len(rows) > maxDataRows {
				return nil, fmt.Errorf("%w: more than %d rows", ErrData, maxDataRows)
			}
		}
	case "json":
		var items []map[string]any
		dec := json.NewDecoder(strings.NewReader(f.Content))
		dec.UseNumber()
		if err := dec.Decode(&items); err != nil {
			return nil, fmt.Errorf("%w: JSON must be an array of objects: %v", ErrData, err)
		}
		set := map[string]bool{}
		for _, it := range items {
			for k := range it {
				if !set[k] {
					set[k] = true
					cols = append(cols, k)
				}
			}
		}
		sort.Strings(cols)
		for _, it := range items {
			rec := make([]string, len(cols))
			for i, c := range cols {
				if v, ok := it[c]; ok && v != nil {
					rec[i] = fmt.Sprint(v)
				}
			}
			rows = append(rows, rec)
		}
		if len(rows) > maxDataRows {
			return nil, fmt.Errorf("%w: more than %d rows", ErrData, maxDataRows)
		}
	default:
		return nil, fmt.Errorf("%w: unsupported format %q (csv, tsv, json)", ErrData, format)
	}
	if len(cols) > maxDataColumns {
		return nil, fmt.Errorf("%w: more than %d columns", ErrData, maxDataColumns)
	}
	sum := &DataSummary{Path: f.Path, Format: format, Columns: cols, RowCount: len(rows), Numeric: map[string]ColumnStats{}, SHA256: f.SHA256}
	for i, r := range rows {
		if i >= sampleRows {
			break
		}
		sum.Sample = append(sum.Sample, r)
	}
	for ci, c := range cols {
		var total, lo, hi *big.Rat
		n, numeric := 0, true
		for _, r := range rows {
			if ci >= len(r) || strings.TrimSpace(r[ci]) == "" {
				continue
			}
			v, ok := new(big.Rat).SetString(strings.ReplaceAll(strings.TrimSpace(r[ci]), ",", ""))
			if !ok {
				numeric = false
				break
			}
			if total == nil {
				total, lo, hi = new(big.Rat), v, v
			}
			total.Add(total, v)
			if v.Cmp(lo) < 0 {
				lo = v
			}
			if v.Cmp(hi) > 0 {
				hi = v
			}
			n++
		}
		if !numeric || n == 0 {
			continue
		}
		mean := new(big.Rat).Quo(total, new(big.Rat).SetInt64(int64(n)))
		s, _ := FormatRat(total, 10)
		mn, _ := FormatRat(lo, 10)
		mx, _ := FormatRat(hi, 10)
		me, _ := FormatRat(mean, 10)
		sum.Numeric[c] = ColumnStats{Count: n, Sum: s, Min: mn, Max: mx, Mean: me}
	}
	return sum, nil
}
