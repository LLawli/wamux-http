package media

import (
	"bytes"
	"context"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// Preview is what a page says about itself in its <head>.
type Preview struct {
	Title       string
	Description string
	Image       string // absolute URL; "" when the page names none
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"]+`)

// FirstURL is the first http(s) URL in a text, without trailing
// punctuation that more likely closes the sentence than the URL.
func FirstURL(text string) string {
	u := urlRe.FindString(text)
	return strings.TrimRight(u, ".,;:!?)]}'")
}

// ParsePreview reads OpenGraph tags from an HTML page, falling back to
// <title> and the description meta. Relative image URLs resolve against
// base.
func ParsePreview(base *url.URL, page []byte) Preview {
	var p, fallback Preview
	z := html.NewTokenizer(bytes.NewReader(page))
	inTitle := false
	for {
		switch z.Next() {
		case html.ErrorToken:
			return merge(p, fallback, base)
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch atom.Lookup(name) {
			case atom.Title:
				inTitle = true
			case atom.Body:
				return merge(p, fallback, base)
			case atom.Meta:
				var key, content string
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					switch strings.ToLower(string(k)) {
					case "property", "name":
						if key == "" || strings.HasPrefix(strings.ToLower(string(v)), "og:") {
							key = strings.ToLower(string(v))
						}
					case "content":
						content = strings.TrimSpace(string(v))
					}
				}
				switch key {
				case "og:title":
					p.Title = content
				case "og:description":
					p.Description = content
				case "og:image", "og:image:url", "og:image:secure_url":
					if p.Image == "" {
						p.Image = content
					}
				case "description", "twitter:description":
					if fallback.Description == "" {
						fallback.Description = content
					}
				case "twitter:title":
					if fallback.Title == "" {
						fallback.Title = content
					}
				case "twitter:image":
					if fallback.Image == "" {
						fallback.Image = content
					}
				}
			}
		case html.TextToken:
			if inTitle && fallback.Title == "" {
				fallback.Title = strings.TrimSpace(html.UnescapeString(string(z.Text())))
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch atom.Lookup(name) {
			case atom.Title:
				inTitle = false
			case atom.Head:
				return merge(p, fallback, base)
			}
		}
	}
}

func merge(p, fallback Preview, base *url.URL) Preview {
	if p.Title == "" {
		p.Title = fallback.Title
	}
	if p.Description == "" {
		p.Description = fallback.Description
	}
	if p.Image == "" {
		p.Image = fallback.Image
	}
	if p.Image != "" && base != nil {
		if u, err := base.Parse(p.Image); err == nil {
			p.Image = u.String()
		} else {
			p.Image = ""
		}
	}
	return p
}

// Link preview limits: how much of a page is read, how long the whole
// preview may take.
const (
	previewPageBytes = 512 << 10
	previewTimeout   = 10 * time.Second
)

// LinkPreview builds the preview card for the first URL in a text: the
// page's title and description and a JPEG thumbnail of its image, every
// fetch through the guard. It returns nil when there is no URL or the page
// gives nothing to show; a missing image only drops the thumbnail.
func (s *Service) LinkPreview(ctx context.Context, text string) *pb.LinkPreview {
	link := FirstURL(text)
	if link == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, previewTimeout)
	defer cancel()
	page, err := s.Fetcher().GetHead(ctx, link, previewPageBytes)
	if err != nil || (page.ContentType != "" && page.ContentType != "text/html" && page.ContentType != "application/xhtml+xml") {
		return nil
	}
	p := ParsePreview(page.URL, page.Data)
	if p.Title == "" && p.Description == "" {
		return nil
	}
	lp := &pb.LinkPreview{MatchedText: link, Title: p.Title, Description: p.Description}
	if p.Image != "" {
		lp.JpegThumbnail = s.thumbnailOf(ctx, p.Image)
	}
	return lp
}

// thumbnailOf fetches an image and scales it to a JPEG thumbnail; nil on any
// failure.
func (s *Service) thumbnailOf(ctx context.Context, imageURL string) []byte {
	img, err := s.Fetcher().Get(ctx, imageURL)
	if err != nil {
		return nil
	}
	return s.Thumbnail(ctx, img.Data)
}

// Thumbnail is a JPEG thumbnail of image bytes, nil when they cannot be
// read.
func (s *Service) Thumbnail(ctx context.Context, data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	thumb, err := s.Converter().Thumbnail(ctx, data)
	if err != nil {
		return nil
	}
	return thumb
}
