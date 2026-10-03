package api

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
)

// ReadData resolves a --data value: "@file" reads a file, "-" reads stdin,
// anything else is used literally.
func ReadData(spec string, stdin io.Reader) ([]byte, error) {
	switch {
	case spec == "-":
		b, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("failed to read --data from stdin: %w", err)
		}
		return b, nil
	case strings.HasPrefix(spec, "@"):
		b, err := os.ReadFile(spec[1:])
		if err != nil {
			return nil, fmt.Errorf("failed to read --data file: %w", err)
		}
		return b, nil
	}
	return []byte(spec), nil
}

// FormField is one --form k=v or k=@file[;type=mime][;filename=name] value.
type FormField struct {
	Name        string
	Value       string // literal value (when File == "")
	File        string // path to upload
	FileName    string
	ContentType string
}

// ParseFormField parses curl-style form syntax:
//
//	name=value
//	name=@path/to/file
//	name=@path/to/file;type=application/javascript+module
//	name=@path;filename=worker.js;type=text/javascript
//
// A literal value may carry ";type=..." too (e.g. metadata=...;type=application/json).
func ParseFormField(s string) (FormField, error) {
	name, val, ok := strings.Cut(s, "=")
	if !ok || name == "" {
		return FormField{}, fmt.Errorf("invalid --form %q: want name=value or name=@file", s)
	}
	f := FormField{Name: name}
	if strings.HasPrefix(val, "@") {
		parts := strings.Split(val[1:], ";")
		f.File = parts[0]
		for _, p := range parts[1:] {
			k, v, _ := strings.Cut(p, "=")
			switch strings.TrimSpace(k) {
			case "type":
				f.ContentType = v
			case "filename":
				f.FileName = v
			default:
				return FormField{}, fmt.Errorf("invalid --form %q: unknown option %q", s, k)
			}
		}
		if f.File == "" {
			return FormField{}, fmt.Errorf("invalid --form %q: missing file path", s)
		}
		if f.FileName == "" {
			f.FileName = filepath.Base(f.File)
		}
		return f, nil
	}
	if i := strings.LastIndex(val, ";type="); i >= 0 {
		f.ContentType = val[i+len(";type="):]
		val = val[:i]
	}
	f.Value = val
	return f, nil
}

// BuildMultipart encodes form fields as multipart/form-data and returns the
// body and its Content-Type (with boundary).
func BuildMultipart(fields []FormField) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range fields {
		h := textproto.MIMEHeader{}
		if f.File != "" {
			data, err := os.ReadFile(f.File)
			if err != nil {
				return nil, "", fmt.Errorf("failed to read --form file for %q: %w", f.Name, err)
			}
			ct := f.ContentType
			if ct == "" {
				ct = mime.TypeByExtension(filepath.Ext(f.File))
			}
			if ct == "" {
				ct = "application/octet-stream"
			}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, f.Name, f.FileName))
			h.Set("Content-Type", ct)
			pw, err := w.CreatePart(h)
			if err != nil {
				return nil, "", err
			}
			if _, err := pw.Write(data); err != nil {
				return nil, "", err
			}
			continue
		}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q`, f.Name))
		if f.ContentType != "" {
			h.Set("Content-Type", f.ContentType)
		}
		pw, err := w.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err := io.WriteString(pw, f.Value); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}
