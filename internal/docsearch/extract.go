package docsearch

import (
	"archive/zip"
	"bytes"
	"context"
	_ "embed"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"

	"prism/internal/swiftbin"
)

//go:embed docshelper/docs.swift
var docsSource []byte

// NewDocsHelper returns the PDF/OCR helper (PDFKit + Vision), compiled on first use into dataDir/bin.
func NewDocsHelper(dataDir string) *swiftbin.Tool {
	return &swiftbin.Tool{Name: "prism-docs", Source: docsSource, Dir: filepath.Join(dataDir, "bin"), Timeout: 10 * time.Minute}
}

// Formats a source can hold beyond plain text. Anything else is skipped.
var (
	textExts  = map[string]bool{".md": true, ".markdown": true, ".txt": true, ".text": true, ".csv": true, ".tsv": true, ".json": true, ".log": true, ".rst": true, ".org": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".xml": true, ".tex": true}
	imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".heic": true, ".tif": true, ".tiff": true, ".gif": true, ".webp": true, ".bmp": true}
)

// DocGlobs are the file patterns of a source that takes documents of every supported kind.
var DocGlobs = []string{"*.md", "*.txt", "*.pdf", "*.docx", "*.epub", "*.odt", "*.rtf", "*.html", "*.htm", "*.png", "*.jpg", "*.jpeg", "*.heic", "*.tiff", "*.csv"}

const (
	maxTextFile   = 1 << 20  // plain text files
	maxBinaryFile = 80 << 20 // PDFs, office files, pictures
	maxExtracted  = 6 << 20  // characters kept from one document
)

// maxSize is the biggest file we will read for this extension.
func maxSize(ext string) int64 {
	if textExts[ext] {
		return maxTextFile
	}
	return maxBinaryFile
}

// Extractor turns a file into text. Docs is the optional macOS helper (PDF text, OCR); without it PDFs fall
// back to `pdftotext` when installed, and pictures are skipped.
type Extractor struct {
	Docs *swiftbin.Tool
}

// Supported reports whether a file name is a kind we can read.
func Supported(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".pdf", ".docx", ".epub", ".odt", ".rtf", ".doc", ".html", ".htm", ".xhtml":
		return true
	}
	return textExts[ext] || imageExts[ext]
}

// Text extracts the readable text of a file. ok=false means "nothing to index" (not an error worth reporting).
func (e *Extractor) Text(ctx context.Context, p string) (text string, err error) {
	ext := strings.ToLower(filepath.Ext(p))
	switch {
	case textExts[ext] || ext == "":
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		if !utf8.Valid(b) {
			return "", errors.New("not a UTF-8 text file")
		}
		text = string(b)
	case ext == ".html" || ext == ".htm" || ext == ".xhtml":
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		text = htmlText(b)
	case ext == ".docx":
		text, err = docxText(p)
	case ext == ".odt":
		text, err = odtText(p)
	case ext == ".epub":
		text, err = epubText(p)
	case ext == ".rtf" || ext == ".doc":
		text, err = textutil(ctx, p)
	case ext == ".pdf":
		text, err = e.pdfText(ctx, p)
	case imageExts[ext]:
		text, err = e.ocr(ctx, p)
	default:
		return "", fmt.Errorf("unsupported file type %s", ext)
	}
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if len(text) > maxExtracted {
		text = text[:maxExtracted]
	}
	return text, nil
}

func (e *Extractor) pdfText(ctx context.Context, p string) (string, error) {
	if e != nil && e.Docs != nil && e.Docs.CanBuild() {
		var r struct {
			Text string `json:"text"`
		}
		if err := e.Docs.Run(ctx, "pdf", map[string]any{"path": p}, &r); err != nil {
			return "", err
		}
		return r.Text, nil
	}
	if bin, err := exec.LookPath("pdftotext"); err == nil {
		out, err := exec.CommandContext(ctx, bin, "-layout", "-enc", "UTF-8", p, "-").Output()
		return string(out), err
	}
	return "", errors.New("reading PDFs needs macOS (built in) or `pdftotext` (brew install poppler)")
}

func (e *Extractor) ocr(ctx context.Context, p string) (string, error) {
	if e == nil || e.Docs == nil || !e.Docs.CanBuild() {
		return "", errors.New("reading text from pictures needs macOS")
	}
	var r struct {
		Text string `json:"text"`
	}
	if err := e.Docs.Run(ctx, "ocr", map[string]any{"path": p}, &r); err != nil {
		return "", err
	}
	return r.Text, nil
}

// textutil converts RTF/DOC with macOS' built-in converter.
func textutil(ctx context.Context, p string) (string, error) {
	bin, err := exec.LookPath("textutil")
	if err != nil {
		return "", errors.New("this file type needs macOS (textutil)")
	}
	out, err := exec.CommandContext(ctx, bin, "-convert", "txt", "-stdout", p).Output()
	return string(out), err
}

// ── HTML ────────────────────────────────────────────────────────────────────

var blockTags = map[string]bool{"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "section": true, "article": true, "blockquote": true, "pre": true, "table": true, "ul": true, "ol": true}

// htmlText flattens HTML to paragraphs of text (scripts, styles and navigation dropped).
func htmlText(b []byte) string {
	doc, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return string(b)
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "nav", "head", "svg":
				return
			}
			if strings.HasPrefix(n.Data, "h") && len(n.Data) == 2 && n.Data[1] >= '1' && n.Data[1] <= '6' {
				sb.WriteString("\n\n" + strings.Repeat("#", int(n.Data[1]-'0')) + " ")
			}
		}
		if n.Type == html.TextNode {
			sb.WriteString(strings.Join(strings.Fields(n.Data), " ") + " ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockTags[n.Data] {
			sb.WriteString("\n\n")
		}
	}
	walk(doc)
	return tidy(sb.String())
}

func tidy(s string) string {
	var out []string
	blank := 0
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			blank++
			if blank == 1 {
				out = append(out, "")
			}
			continue
		}
		blank = 0
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// ── zip-based formats ───────────────────────────────────────────────────────

func readZipFile(zr *zip.ReadCloser, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 64<<20)) // a zip bomb cannot exhaust memory
		}
	}
	return nil, fmt.Errorf("%s not found in the archive", name)
}

// xmlText collects character data, adding a newline at the end of every element named in breaks.
func xmlText(b []byte, breaks map[string]bool, tabs map[string]bool) string {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(t)) == "" && bytes.Contains(t, []byte("\n")) {
				continue // indentation between elements, not content
			}
			sb.Write(t)
		case xml.StartElement:
			if tabs[t.Name.Local] {
				sb.WriteString("\t")
			}
			if t.Name.Local == "br" || t.Name.Local == "line-break" {
				sb.WriteString("\n")
			}
		case xml.EndElement:
			if breaks[t.Name.Local] {
				sb.WriteString("\n")
			}
		}
	}
	return tidy(sb.String())
}

func docxText(p string) (string, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return "", fmt.Errorf("not a valid .docx: %w", err)
	}
	defer zr.Close()
	b, err := readZipFile(zr, "word/document.xml")
	if err != nil {
		return "", err
	}
	// only <w:t> holds text; without this, XML attribute-less structure tags would add nothing but noise anyway
	return xmlText(b, map[string]bool{"p": true}, map[string]bool{"tab": true}), nil
}

func odtText(p string) (string, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return "", fmt.Errorf("not a valid .odt: %w", err)
	}
	defer zr.Close()
	b, err := readZipFile(zr, "content.xml")
	if err != nil {
		return "", err
	}
	return xmlText(b, map[string]bool{"p": true, "h": true}, map[string]bool{"tab": true}), nil
}

// epubText reads the chapters in spine (reading) order.
func epubText(p string) (string, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return "", fmt.Errorf("not a valid .epub: %w", err)
	}
	defer zr.Close()
	container, err := readZipFile(zr, "META-INF/container.xml")
	if err != nil {
		return "", err
	}
	var c struct {
		Root struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.Unmarshal(container, &c); err != nil || c.Root.Path == "" {
		return "", errors.New("the EPUB has no package file")
	}
	opfBytes, err := readZipFile(zr, c.Root.Path)
	if err != nil {
		return "", err
	}
	var opf struct {
		Manifest []struct {
			ID   string `xml:"id,attr"`
			Href string `xml:"href,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	if err := xml.Unmarshal(opfBytes, &opf); err != nil {
		return "", fmt.Errorf("unreadable EPUB package: %w", err)
	}
	href := map[string]string{}
	for _, m := range opf.Manifest {
		href[m.ID] = m.Href
	}
	base := path.Dir(c.Root.Path)
	var order []string
	for _, s := range opf.Spine {
		if h := href[s.IDRef]; h != "" {
			order = append(order, path.Clean(path.Join(base, h)))
		}
	}
	if len(order) == 0 { // no spine: every html file by name
		for _, f := range zr.File {
			if l := strings.ToLower(f.Name); strings.HasSuffix(l, ".xhtml") || strings.HasSuffix(l, ".html") {
				order = append(order, f.Name)
			}
		}
		sort.Strings(order)
	}
	var sb strings.Builder
	for _, name := range order {
		b, err := readZipFile(zr, name)
		if err != nil {
			continue
		}
		sb.WriteString(htmlText(b) + "\n\n")
		if sb.Len() > maxExtracted {
			break
		}
	}
	return sb.String(), nil
}
