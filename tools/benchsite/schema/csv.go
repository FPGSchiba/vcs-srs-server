package schema

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
)

// validator is implemented by every row type: it rejects a value outside the
// schema's enums and patterns.
type validator interface{ Validate() error }

// Header returns the column names of T, from its `csv` tags in field order.
func Header[T any]() []string {
	t := reflect.TypeFor[T]()
	h := make([]string, t.NumField())
	for i := range h {
		h[i] = t.Field(i).Tag.Get("csv")
	}
	return h
}

// ReadCSV decodes rows of T. The first record must equal header exactly (and
// header must be T's own), every row must have exactly that many cells, and an
// empty cell of a pointer field decodes to nil. Each row is then validated.
func ReadCSV[T any](r io.Reader, header []string) ([]T, error) {
	if want := Header[T](); !slices.Equal(want, header) {
		return nil, fmt.Errorf("csv: expected header %v is not the schema's %v", header, want)
	}
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = len(header)
	got, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("csv header: %w", err)
	}
	if !slices.Equal(got, header) {
		return nil, fmt.Errorf("csv: header %v does not match the schema %v", got, header)
	}
	var out []T
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("csv line %d: %w", line, err)
		}
		var row T
		v := reflect.ValueOf(&row).Elem()
		for i, cell := range rec {
			if err := setCell(v.Field(i), cell); err != nil {
				return nil, fmt.Errorf("csv line %d column %s: %w", line, header[i], err)
			}
		}
		if vd, ok := any(row).(validator); ok {
			if err := vd.Validate(); err != nil {
				return nil, fmt.Errorf("csv line %d: %w", line, err)
			}
		}
		out = append(out, row)
	}
}

// WriteCSV validates and encodes rows with T's header.
func WriteCSV[T any](rows []T) ([]byte, error) {
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	if err := cw.Write(Header[T]()); err != nil {
		return nil, err
	}
	for n, row := range rows {
		if vd, ok := any(row).(validator); ok {
			if err := vd.Validate(); err != nil {
				return nil, fmt.Errorf("row %d: %w", n+1, err)
			}
		}
		v := reflect.ValueOf(row)
		rec := make([]string, v.NumField())
		for i := range rec {
			rec[i] = formatCell(v.Field(i))
		}
		if err := cw.Write(rec); err != nil {
			return nil, err
		}
	}
	cw.Flush()
	return buf.Bytes(), cw.Error()
}

func formatCell(v reflect.Value) string {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)
	case reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	}
	panic("schema: unsupported csv field kind " + v.Kind().String())
}

func setCell(v reflect.Value, cell string) error {
	if v.Kind() == reflect.Pointer {
		if cell == "" {
			return nil
		}
		p := reflect.New(v.Type().Elem())
		if err := setCell(p.Elem(), cell); err != nil {
			return err
		}
		v.Set(p)
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(cell)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(cell, 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Uint64:
		n, err := strconv.ParseUint(cell, 10, 64)
		if err != nil {
			return err
		}
		v.SetUint(n)
	case reflect.Float64:
		f, err := strconv.ParseFloat(cell, 64)
		if err != nil {
			return err
		}
		v.SetFloat(f)
	case reflect.Bool:
		b, err := strconv.ParseBool(cell)
		if err != nil {
			return err
		}
		v.SetBool(b)
	default:
		return fmt.Errorf("unsupported field kind %s", v.Kind())
	}
	return nil
}
