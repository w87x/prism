package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"prism/internal/netguard"
	"prism/internal/settings"
	"prism/internal/tools"
)

// Pictures from the web reach the user only as artifacts saved on PRISM's side, never as a remote <img> the browser
// would load by itself: a poisoned page could otherwise make the assistant write an image address that carries the
// conversation's secrets in its query string, and every remote image also tells its host who is reading.
const (
	maxFetchedImage  = 8 << 20
	imageArtifactTTL = MaxArtifactTTL
)

// rasterKind reports the type of an image from its bytes: only formats a browser shows safely (no SVG, which can
// carry script).
func rasterKind(b []byte) (mime, ext string) {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png", ".png"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg", ".jpg"
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "image/gif", ".gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp", ".webp"
	}
	return "", ""
}

// FetchImage downloads a picture through the private-address guard and saves it as an artifact.
func FetchImage(ctx context.Context, d Deps, rawURL, by string) (*Artifact, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", fmt.Errorf("invalid image URL %q (http/https only)", rawURL)
	}
	allow := settings.Load(ctx, d.Settings, settings.KeyWeb, settings.Web{}).AllowPrivate
	if !allow {
		if err := netguard.CheckURL(ctx, u.String()); err != nil {
			return nil, "", err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; PRISM image fetch)")
	req.Header.Set("Accept", "image/png,image/jpeg,image/gif,image/webp,image/*;q=0.8")
	resp, err := netguard.Client(allow, 30*time.Second).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("the image server answered HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxFetchedImage {
		return nil, "", fmt.Errorf("the image is larger than %d MB", maxFetchedImage>>20)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchedImage+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > maxFetchedImage {
		return nil, "", fmt.Errorf("the image is larger than %d MB", maxFetchedImage>>20)
	}
	mime, ext := rasterKind(b)
	if mime == "" {
		return nil, "", errors.New("that URL is not a PNG, JPEG, GIF or WebP picture (SVG and other formats are not shown)")
	}
	name := path.Base(u.Path)
	if name == "" || name == "." || name == "/" {
		name = "image"
	}
	if i := strings.LastIndex(name, "."); i > 0 {
		name = name[:i]
	}
	art, err := SaveArtifactOpts(ctx, d, name+ext, mime, b, by, ArtifactOpts{TTL: imageArtifactTTL, Tainted: true})
	if err != nil {
		return nil, "", err
	}
	return art, strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), nil
}

func registerImageFetch(reg *tools.Registry, d Deps) {
	reg.Register(&tools.Tool{
		Name: "image_fetch", Category: "web", Base: true, Risk: tools.RiskWrite, Auto: true,
		Description: "Show the user a picture from the web: downloads the image at a URL (PNG, JPEG, GIF or WebP, up to 8 MB) and saves it, then returns a marker like [image:12] — put that marker in your reply and the chat displays the picture (keep it for a week). Pasting a raw image link or ![](url) does NOT show a picture. When you have read web content, the URL must be one that appeared in it (find image URLs with web_media).",
		Params:      tools.Obj("url", tools.Str("url", "address of the image file"), tools.Str("caption", "what it shows (optional, shown under the picture)")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct {
				URL     string `json:"url"`
				Caption string `json:"caption"`
			}](raw)
			if err != nil {
				return "", err
			}
			if env.Tainted && !env.Sources.HasURL(a.URL) {
				return "", errors.New("after reading web content, only image addresses that appeared in its results can be fetched: find them with web_media or web_fetch first")
			}
			art, host, err := FetchImage(ctx, d, a.URL, env.Agent)
			if err != nil {
				return "", err
			}
			marker := fmt.Sprintf("[image:%d]", art.ID)
			return fmt.Sprintf("Saved %s (%s, %d KB, from %s). Put %s in your reply so the user sees it.", art.Name, art.Mime, art.Size/1024, host, marker), nil
		},
	})
}
