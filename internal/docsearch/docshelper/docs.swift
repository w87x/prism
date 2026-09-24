// prism-docs: text out of PDFs and images for PRISM's document ingestion (PDFKit for text PDFs, Vision OCR
// for scans and pictures). One JSON argument in, one JSON document out.
//
//   prism-docs selftest '{}'
//   prism-docs pdf '{"path": "/x/y.pdf", "max_pages": 400}'   → {"text": "...", "pages": N, "ocr_pages": M}
//   prism-docs ocr '{"path": "/x/y.png"}'                      → {"text": "..."}
import Foundation
import PDFKit
import Vision
import CoreGraphics
import ImageIO

func emit(_ obj: Any) {
    guard let d = try? JSONSerialization.data(withJSONObject: obj, options: [.sortedKeys]) else { fail("cannot encode the result") }
    FileHandle.standardOutput.write(d)
    FileHandle.standardOutput.write("\n".data(using: .utf8)!)
}
func fail(_ msg: String) -> Never {
    let d = (try? JSONSerialization.data(withJSONObject: ["error": msg])) ?? Data("{\"error\":\"failed\"}".utf8)
    FileHandle.standardOutput.write(d)
    FileHandle.standardOutput.write("\n".data(using: .utf8)!)
    exit(1)
}

func args() -> [String: Any] {
    guard CommandLine.arguments.count > 2, let d = CommandLine.arguments[2].data(using: .utf8),
          let o = (try? JSONSerialization.jsonObject(with: d)) as? [String: Any] else { return [:] }
    return o
}

func recognize(_ image: CGImage) -> String {
    let req = VNRecognizeTextRequest()
    req.recognitionLevel = .accurate
    req.usesLanguageCorrection = true
    if #available(macOS 13.0, *) { req.automaticallyDetectsLanguage = true }
    let handler = VNImageRequestHandler(cgImage: image, options: [:])
    do { try handler.perform([req]) } catch { return "" }
    let lines = (req.results ?? []).compactMap { $0.topCandidates(1).first?.string }
    return lines.joined(separator: "\n")
}

/// Transparent pictures (screenshots, PNGs) are read as black-on-black by Vision: put them on white first.
func flatten(_ img: CGImage) -> CGImage {
    let w = img.width, h = img.height
    guard let cs = CGColorSpace(name: CGColorSpace.sRGB),
          let ctx = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: 0, space: cs,
                              bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return img }
    ctx.setFillColor(CGColor(red: 1, green: 1, blue: 1, alpha: 1))
    ctx.fill(CGRect(x: 0, y: 0, width: w, height: h))
    ctx.draw(img, in: CGRect(x: 0, y: 0, width: w, height: h))
    return ctx.makeImage() ?? img
}

func render(_ page: PDFPage, scale: CGFloat) -> CGImage? {
    let r = page.bounds(for: .mediaBox)
    let w = Int(r.width * scale), h = Int(r.height * scale)
    guard w > 0, h > 0, w < 12000, h < 12000, let cs = CGColorSpace(name: CGColorSpace.sRGB),
          let ctx = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: 0, space: cs,
                              bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return nil }
    ctx.setFillColor(CGColor(red: 1, green: 1, blue: 1, alpha: 1))
    ctx.fill(CGRect(x: 0, y: 0, width: w, height: h))
    ctx.scaleBy(x: scale, y: scale)
    page.draw(with: .mediaBox, to: ctx)
    return ctx.makeImage()
}

let a = args()
guard CommandLine.arguments.count > 1 else { fail("usage: prism-docs <command> '<json>'") }

switch CommandLine.arguments[1] {
case "selftest":
    emit(["ok": true, "version": 1])

case "pdf":
    guard let path = a["path"] as? String else { fail("path is required") }
    guard let doc = PDFDocument(url: URL(fileURLWithPath: path)) else { fail("cannot open the PDF (damaged, or a different file type)") }
    if doc.isLocked && !doc.unlock(withPassword: "") { fail("the PDF is password-protected") }
    let maxPages = min(max((a["max_pages"] as? Int) ?? 400, 1), 2000)
    var out: [String] = []
    var ocrPages = 0
    let n = min(doc.pageCount, maxPages)
    for i in 0..<n {
        guard let page = doc.page(at: i) else { continue }
        var t = (page.string ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if t.count < 25, let img = render(page, scale: 2.2) { // a scan (or a picture page): read it with Vision
            let o = recognize(img).trimmingCharacters(in: .whitespacesAndNewlines)
            if o.count > t.count { t = o; ocrPages += 1 }
        }
        if !t.isEmpty { out.append(n > 1 ? "[page \(i + 1)]\n\(t)" : t) }
    }
    emit(["text": out.joined(separator: "\n\n"), "pages": doc.pageCount, "ocr_pages": ocrPages, "truncated": doc.pageCount > maxPages])

case "ocr":
    guard let path = a["path"] as? String else { fail("path is required") }
    guard let src = CGImageSourceCreateWithURL(URL(fileURLWithPath: path) as CFURL, nil),
          let img = CGImageSourceCreateImageAtIndex(src, 0, nil) else { fail("cannot read the image") }
    emit(["text": recognize(flatten(img)).trimmingCharacters(in: .whitespacesAndNewlines)])

default:
    fail("unknown command \(CommandLine.arguments[1])")
}
