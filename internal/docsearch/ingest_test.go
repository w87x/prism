package docsearch

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"prism/internal/testutil"
	"prism/internal/tools"
)

func makeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

const docxBody = `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Quarterly report</w:t></w:r></w:p>
<w:p><w:r><w:t xml:space="preserve">Revenue grew </w:t></w:r><w:r><w:t>12 percent.</w:t></w:r></w:p>
<w:p><w:r><w:t>Lighthouse delayed</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>until October.</w:t></w:r></w:p></w:body></w:document>`

func TestExtractorsReadOfficeAndWebFormats(t *testing.T) {
	dir := t.TempDir()
	e := &Extractor{}
	ctx := context.Background()

	docx := filepath.Join(dir, "r.docx")
	makeZip(t, docx, map[string]string{"word/document.xml": docxBody})
	got, err := e.Text(ctx, docx)
	if err != nil || !strings.Contains(got, "Quarterly report\nRevenue grew 12 percent.\nLighthouse delayed\tuntil October.") {
		t.Fatalf("docx: %q %v", got, err)
	}

	odt := filepath.Join(dir, "r.odt")
	makeZip(t, odt, map[string]string{"content.xml": `<office:document-content xmlns:office="o" xmlns:text="t"><office:body><text:h>Title</text:h><text:p>First <text:span>para</text:span></text:p></office:body></office:document-content>`})
	if got, err := e.Text(ctx, odt); err != nil || got != "Title\nFirst para" {
		t.Fatalf("odt: %q %v", got, err)
	}

	// an EPUB is read in spine order, not file-name order
	epub := filepath.Join(dir, "b.epub")
	makeZip(t, epub, map[string]string{
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`,
		"OEBPS/content.opf":      `<package><manifest><item id="a" href="a.xhtml"/><item id="b" href="b.xhtml"/></manifest><spine><itemref idref="b"/><itemref idref="a"/></spine></package>`,
		"OEBPS/a.xhtml":          `<html><body><h1>Second chapter</h1><p>The end.</p></body></html>`,
		"OEBPS/b.xhtml":          `<html><body><h1>First chapter</h1><p>Once upon a time.</p></body></html>`,
	})
	got, err = e.Text(ctx, epub)
	if err != nil || strings.Index(got, "First chapter") > strings.Index(got, "Second chapter") || !strings.Contains(got, "Once upon a time.") {
		t.Fatalf("epub: %q %v", got, err)
	}

	page := filepath.Join(dir, "p.html")
	_ = os.WriteFile(page, []byte(`<html><head><title>x</title><style>p{color:red}</style></head><body><nav>MENU</nav><script>alert(1)</script><h2>Heading</h2><p>Some <b>bold</b> text.</p><ul><li>one</li><li>two</li></ul></body></html>`), 0o644)
	got, err = e.Text(ctx, page)
	if err != nil || strings.Contains(got, "alert") || strings.Contains(got, "MENU") || strings.Contains(got, "color:red") || !strings.Contains(got, "## Heading") || !strings.Contains(got, "Some bold text.") {
		t.Fatalf("html: %q %v", got, err)
	}

	// broken or wrong files fail cleanly instead of panicking
	bad := filepath.Join(dir, "bad.docx")
	_ = os.WriteFile(bad, []byte("not a zip"), 0o644)
	if _, err := e.Text(ctx, bad); err == nil {
		t.Fatal("a corrupt docx must be an error")
	}
	bin := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(bin, []byte{0xff, 0xfe, 0x00, 0x80}, 0o644)
	if _, err := e.Text(ctx, bin); err == nil {
		t.Fatal("binary data posing as text must be an error")
	}
	if Supported("malware.exe") || !Supported("Paper.PDF") || !Supported("scan.HEIC") {
		t.Fatal("Supported()")
	}
}

// PDFs and pictures go through the PDFKit/Vision helper (macOS): a text PDF, a scanned PDF (OCR) and a picture.
func TestPDFAndOCRWithTheMacHelper(t *testing.T) {
	if runtime.GOOS != "darwin" || testing.Short() {
		t.Skip("macOS only")
	}
	for _, tool := range []string{"cupsfilter", "sips", "swiftc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("needs " + tool)
		}
	}
	dir := t.TempDir()
	txt := filepath.Join(dir, "t.txt")
	_ = os.WriteFile(txt, []byte("Quarterly report\n\nRevenue grew twelve percent.\nThe lighthouse project was\ndelayed until October.\n"), 0o644)
	pdf, _ := exec.Command("cupsfilter", "-o", "cpi=6", "-o", "lpi=4", "-m", "application/pdf", txt).Output() // big type: OCR of tiny scans is unreliable
	textPDF := filepath.Join(dir, "text.pdf")
	_ = os.WriteFile(textPDF, pdf, 0o644)
	png := filepath.Join(dir, "page.png")
	scan := filepath.Join(dir, "scan.pdf")
	if err := exec.Command("sips", "-s", "format", "png", "-Z", "1600", textPDF, "--out", png).Run(); err != nil {
		t.Skip("sips could not render the PDF")
	}
	_ = exec.Command("sips", "-s", "format", "pdf", png, "--out", scan).Run()

	e := &Extractor{Docs: NewDocsHelper(t.TempDir())}
	ctx := context.Background()
	got, err := e.Text(ctx, textPDF)
	if err != nil || !strings.Contains(got, "lighthouse project") || !strings.Contains(got, "Quarterly report") {
		t.Errorf("text pdf: %q %v", got, err)
	}
	// OCR is approximate: check for the distinctive words only
	for name, p := range map[string]string{"scanned pdf (OCR)": scan, "picture (OCR)": png} {
		got, err := e.Text(ctx, p)
		if err != nil || !strings.Contains(got, "Quarterly") || !strings.Contains(strings.ToLower(got), "october") {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	if _, err := e.Text(ctx, filepath.Join(dir, "t.txt")+".pdf"); err == nil {
		t.Error("a missing PDF must be an error")
	}
}

func TestInboxIngestSearchAndTrust(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	data := t.TempDir()
	inbox := filepath.Join(data, "inbox")
	s := &Service{DB: d.Pool, VectorOn: d.VectorOn, LLM: r, Extract: &Extractor{}, InboxDir: inbox}
	ctx := context.Background()
	docx := filepath.Join(t.TempDir(), "r.docx")
	makeZip(t, docx, map[string]string{"word/document.xml": docxBody})
	b, _ := os.ReadFile(docx)

	name, chunks, err := s.Ingest(ctx, "../../etc/Report Q3.docx", b)
	if err != nil || chunks == 0 || name != "Report Q3.docx" {
		t.Fatalf("ingest: %q %d %v", name, chunks, err)
	}
	if _, err := os.Stat(filepath.Join(inbox, "Report Q3.docx")); err != nil {
		t.Fatalf("the file must land inside the inbox, whatever name it arrived with: %v", err)
	}
	// the same name again does not overwrite
	if n2, _, err := s.Ingest(ctx, "Report Q3.docx", b); err != nil || n2 != "Report Q3 (2).docx" {
		t.Fatalf("second copy: %q %v", n2, err)
	}
	if _, _, err := s.Ingest(ctx, "tool.exe", []byte("MZ")); err == nil {
		t.Fatal("unsupported types are refused")
	}
	if _, _, err := s.Ingest(ctx, "empty.docx", []byte("not a zip")); err == nil {
		t.Fatal("an unreadable document is refused and not kept")
	}
	if left, _ := filepath.Glob(filepath.Join(inbox, "empty*")); len(left) != 0 {
		t.Fatalf("unreadable files must not linger in the inbox: %v", left)
	}

	hits, err := s.Search(ctx, "Revenue grew 12 percent", InboxName, 3)
	if err != nil || len(hits) == 0 || !strings.Contains(hits[0].Text, "Revenue grew") || !hits[0].Untrusted {
		t.Fatalf("search: %+v %v", hits, err)
	}
	// searching outside material taints the turn; the user's own notes do not
	reg := tools.NewRegistry(d.Pool)
	RegisterTools(reg, s)
	run := func(q, src string) bool {
		tainted := false
		env := &tools.Env{Agent: "Atlas", Taint: func() { tainted = true }}
		tool, _ := reg.Get("semantic_search")
		arg, _ := json.Marshal(map[string]any{"query": q, "source": src})
		if _, err := tool.Run(ctx, env, arg); err != nil {
			t.Fatal(err)
		}
		return tainted
	}
	if !run("Revenue grew 12 percent", InboxName) {
		t.Fatal("hits from the Inbox must taint the turn")
	}
	notes := t.TempDir()
	_ = os.WriteFile(filepath.Join(notes, "mine.md"), []byte("Buy oat milk and call the dentist."), 0o644)
	id, _ := s.SaveSource(ctx, Source{Name: "notes", Path: notes})
	_, _, _ = s.Index(ctx, id, nil)
	if run("oat milk dentist", "notes") {
		t.Fatal("the user's own notes are not untrusted")
	}

	// re-saving a file with the same words does not re-embed it; changing the words does
	srcs, _ := s.Sources(ctx)
	var inboxID int64
	for _, x := range srcs {
		if x.Name == InboxName {
			inboxID = x.ID
		}
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(inbox, "Report Q3.docx"), future, future)
	if f, _, _ := s.Index(ctx, inboxID, nil); f != 0 {
		t.Fatalf("an unchanged document must not be re-indexed, re-indexed %d", f)
	}
	makeZip(t, filepath.Join(inbox, "Report Q3.docx"), map[string]string{"word/document.xml": strings.Replace(docxBody, "12 percent", "15 percent", 1)})
	future = future.Add(time.Hour)
	_ = os.Chtimes(filepath.Join(inbox, "Report Q3.docx"), future, future)
	if f, _, _ := s.Index(ctx, inboxID, nil); f != 1 {
		t.Fatalf("a changed document must be re-indexed, got %d", f)
	}
}

func TestDocIngestToolHonoursTheFilePolicy(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	dir := t.TempDir()
	s := &Service{DB: d.Pool, VectorOn: d.VectorOn, LLM: r, Extract: &Extractor{}, InboxDir: filepath.Join(dir, "inbox")}
	reg := tools.NewRegistry(d.Pool)
	RegisterTools(reg, s)
	tool, _ := reg.Get("doc_ingest")
	ctx := context.Background()
	notes := filepath.Join(dir, "notes.md")
	_ = os.WriteFile(notes, []byte("# Trip\n\nBook the ferry to Madeira."), 0o644)
	call := func(p string) (string, error) {
		b, _ := json.Marshal(map[string]any{"path": p})
		return tool.Run(ctx, &tools.Env{Agent: "Atlas"}, b)
	}
	if _, err := call(notes); err == nil {
		t.Fatal("with no file policy configured nothing is ingested")
	}
	s.CanRead = func(ctx context.Context, p string) error {
		if strings.Contains(p, "secret") {
			return errors.New("path is protected")
		}
		return nil
	}
	if out, err := call(notes); err != nil || !strings.Contains(out, "notes.md") {
		t.Fatalf("allowed file: %q %v", out, err)
	}
	if _, err := call(filepath.Join(dir, "secret.txt")); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("a protected path must be refused: %v", err)
	}
	if _, err := call("relative/path.md"); err == nil {
		t.Fatal("relative paths are refused")
	}
}

func TestDocReadToolPagesTextAndTaintsOutsideUploads(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	data := t.TempDir()
	s := &Service{DB: d.Pool, VectorOn: d.VectorOn, LLM: r, Extract: &Extractor{}, InboxDir: filepath.Join(data, "inbox")}
	s.CanRead = func(ctx context.Context, p string) error {
		if strings.Contains(p, "secret") {
			return errors.New("path is protected")
		}
		return nil
	}
	reg := tools.NewRegistry(d.Pool)
	RegisterTools(reg, s)
	tool, _ := reg.Get("doc_read")
	ctx := context.Background()

	uploads := filepath.Join(data, "work", "uploads", "20260922")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(uploads, "report.docx")
	makeZip(t, mine, map[string]string{"word/document.xml": docxBody})
	elsewhere := filepath.Join(t.TempDir(), "downloaded.docx")
	makeZip(t, elsewhere, map[string]string{"word/document.xml": docxBody})

	tainted := false
	call := func(args map[string]any, path string) (string, error) {
		args["path"] = path
		b, _ := json.Marshal(args)
		tainted = false
		return tool.Run(ctx, &tools.Env{Agent: "Atlas", Taint: func() { tainted = true }}, b)
	}
	out, err := call(map[string]any{}, mine)
	if err != nil || !strings.Contains(out, "Revenue grew 12 percent.") {
		t.Fatalf("read: %q %v", out, err)
	}
	if tainted {
		t.Fatal("a file the user attached to the chat must not taint the turn")
	}
	if _, err = call(map[string]any{}, elsewhere); err != nil || !tainted {
		t.Fatalf("a document from elsewhere may carry instructions: tainted=%v err=%v", tainted, err)
	}
	// paging: a small window, with a hint how to continue
	out, _ = call(map[string]any{"limit": 10}, mine)
	if !strings.Contains(out, "more characters; continue with offset=10") {
		t.Fatalf("paging hint: %q", out)
	}
	if out, _ = call(map[string]any{"offset": 100000}, mine); !strings.Contains(out, "document has") {
		t.Fatalf("offset past the end: %q", out)
	}
	if _, err = call(map[string]any{}, filepath.Join(data, "secret.docx")); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("policy: %v", err)
	}
	if _, err = call(map[string]any{}, "rel.docx"); err == nil {
		t.Fatal("relative path accepted")
	}
	bin := filepath.Join(uploads, "blob.xyz")
	_ = os.WriteFile(bin, []byte("x"), 0o644)
	if _, err = call(map[string]any{}, bin); err == nil {
		t.Fatal("unsupported type accepted")
	}
}
