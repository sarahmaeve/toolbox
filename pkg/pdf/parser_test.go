package pdf

import (
	"strings"
	"testing"
)

// newTestPDFFile returns a minimal pdfFile suitable for parseValue calls that
// don't require xref resolution.
func newTestPDFFile() *pdfFile {
	return &pdfFile{
		xref:    map[int]xrefEntry{},
		cache:   map[int]any{},
		objStms: map[int]*objStm{},
	}
}

func TestParseValue(t *testing.T) {
	t.Parallel()

	f := newTestPDFFile()

	t.Run("dictionary", func(t *testing.T) {
		t.Parallel()
		data := []byte(`<< /Key (value) /Num 42 >>`)
		val, _, err := parseValue(data, 0, f)
		if err != nil {
			t.Fatalf("parseValue dict: %v", err)
		}
		d, ok := val.(pdfDict)
		if !ok {
			t.Fatalf("expected pdfDict, got %T", val)
		}
		if s, ok := d["Key"].(pdfString); !ok || string(s) != "value" {
			t.Errorf("dict[Key]: got %v (%T), want pdfString(\"value\")", d["Key"], d["Key"])
		}
		if n, ok := d["Num"].(pdfNumber); !ok || n != 42 {
			t.Errorf("dict[Num]: got %v (%T), want pdfNumber(42)", d["Num"], d["Num"])
		}
	})

	t.Run("array", func(t *testing.T) {
		t.Parallel()
		data := []byte(`[1 2 3]`)
		val, _, err := parseValue(data, 0, f)
		if err != nil {
			t.Fatalf("parseValue array: %v", err)
		}
		arr, ok := val.(pdfArray)
		if !ok {
			t.Fatalf("expected pdfArray, got %T", val)
		}
		if len(arr) != 3 {
			t.Fatalf("array length: got %d, want 3", len(arr))
		}
		for i, wantN := range []pdfNumber{1, 2, 3} {
			n, ok := arr[i].(pdfNumber)
			if !ok {
				t.Errorf("arr[%d]: expected pdfNumber, got %T", i, arr[i])
				continue
			}
			if n != wantN {
				t.Errorf("arr[%d]: got %v, want %v", i, n, wantN)
			}
		}
	})

	t.Run("name", func(t *testing.T) {
		t.Parallel()
		data := []byte(`/FlateDecode`)
		val, _, err := parseValue(data, 0, f)
		if err != nil {
			t.Fatalf("parseValue name: %v", err)
		}
		n, ok := val.(pdfName)
		if !ok {
			t.Fatalf("expected pdfName, got %T", val)
		}
		if string(n) != "FlateDecode" {
			t.Errorf("name: got %q, want %q", string(n), "FlateDecode")
		}
	})

	t.Run("literal string with backslash", func(t *testing.T) {
		t.Parallel()
		data := []byte(`(hello \world)`)
		val, _, err := parseValue(data, 0, f)
		if err != nil {
			t.Fatalf("parseValue literal string: %v", err)
		}
		s, ok := val.(pdfString)
		if !ok {
			t.Fatalf("expected pdfString, got %T", val)
		}
		if !strings.Contains(string(s), "hello") || !strings.Contains(string(s), "world") {
			t.Errorf("literal string: got %q, want string containing \"hello\" and \"world\"", string(s))
		}
	})

	t.Run("hex string", func(t *testing.T) {
		t.Parallel()
		data := []byte(`<48656C6C6F>`)
		val, _, err := parseValue(data, 0, f)
		if err != nil {
			t.Fatalf("parseValue hex string: %v", err)
		}
		s, ok := val.(pdfString)
		if !ok {
			t.Fatalf("expected pdfString, got %T", val)
		}
		if string(s) != "Hello" {
			t.Errorf("hex string: got %q, want %q", string(s), "Hello")
		}
	})

	t.Run("indirect reference", func(t *testing.T) {
		t.Parallel()
		data := []byte(`5 0 R`)
		val, _, err := parseValue(data, 0, f)
		if err != nil {
			t.Fatalf("parseValue ref: %v", err)
		}
		ref, ok := val.(pdfRef)
		if !ok {
			t.Fatalf("expected pdfRef, got %T", val)
		}
		if ref.num != 5 || ref.gen != 0 {
			t.Errorf("pdfRef: got {%d, %d}, want {5, 0}", ref.num, ref.gen)
		}
	})

	t.Run("boolean true", func(t *testing.T) {
		t.Parallel()
		data := []byte(`true`)
		val, _, err := parseValue(data, 0, f)
		if err != nil {
			t.Fatalf("parseValue true: %v", err)
		}
		b, ok := val.(pdfBool)
		if !ok {
			t.Fatalf("expected pdfBool, got %T", val)
		}
		if !bool(b) {
			t.Error("expected pdfBool(true)")
		}
	})

	t.Run("boolean false", func(t *testing.T) {
		t.Parallel()
		data := []byte(`false`)
		val, _, err := parseValue(data, 0, f)
		if err != nil {
			t.Fatalf("parseValue false: %v", err)
		}
		b, ok := val.(pdfBool)
		if !ok {
			t.Fatalf("expected pdfBool, got %T", val)
		}
		if bool(b) {
			t.Error("expected pdfBool(false)")
		}
	})

	t.Run("null", func(t *testing.T) {
		t.Parallel()
		data := []byte(`null`)
		val, _, err := parseValue(data, 0, f)
		if err != nil {
			t.Fatalf("parseValue null: %v", err)
		}
		if _, ok := val.(pdfNull); !ok {
			t.Fatalf("expected pdfNull, got %T", val)
		}
	})
}

func TestReadRawNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantStr string
		wantErr bool
	}{
		{name: "bare plus sign returns error", input: "+", wantErr: true},
		{name: "bare minus sign returns error", input: "-", wantErr: true},
		{name: "integer", input: "42", wantStr: "42"},
		{name: "negative float", input: "-3.14", wantStr: "-3.14"},
		{name: "positive integer with plus sign", input: "+7", wantStr: "+7"},
		{name: "zero", input: "0", wantStr: "0"},
		{name: "float without leading digit", input: ".5", wantStr: ".5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := readRawNumber([]byte(tt.input), 0)
			if tt.wantErr {
				if err == nil {
					t.Errorf("readRawNumber(%q): expected error, got %q", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("readRawNumber(%q): unexpected error: %v", tt.input, err)
			}
			if got != tt.wantStr {
				t.Errorf("readRawNumber(%q): got %q, want %q", tt.input, got, tt.wantStr)
			}
		})
	}
}
