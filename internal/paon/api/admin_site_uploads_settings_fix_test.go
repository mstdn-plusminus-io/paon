package api

import (
	"strings"
	"testing"

	xhtml "golang.org/x/net/html"
)

func siteUploadFixAttr(n *xhtml.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func siteUploadFixFind(n *xhtml.Node, match func(*xhtml.Node) bool) []*xhtml.Node {
	var found []*xhtml.Node
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && match(node) {
			found = append(found, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return found
}

func siteUploadFixText(n *xhtml.Node) string {
	var b strings.Builder
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.TextNode {
			b.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return b.String()
}

func assertSiteUploadSettingsFormIntact(t *testing.T, page string, formAction string, uploadPath string) {
	t.Helper()
	doc, err := xhtml.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	if forms := siteUploadFixFind(doc, func(n *xhtml.Node) bool {
		return n.Data == "form" && strings.HasPrefix(siteUploadFixAttr(n, "action"), "/admin/site_uploads/")
	}); len(forms) != 0 {
		t.Fatalf("site upload delete must not be a nested form, found %d", len(forms))
	}
	settingsForms := siteUploadFixFind(doc, func(n *xhtml.Node) bool {
		return n.Data == "form" && siteUploadFixAttr(n, "action") == formAction
	})
	if len(settingsForms) != 1 {
		t.Fatalf("settings forms for %s = %d, want 1", formAction, len(settingsForms))
	}
	form := settingsForms[0]
	saves := siteUploadFixFind(form, func(n *xhtml.Node) bool {
		return n.Data == "button" && siteUploadFixAttr(n, "type") == "submit" && strings.Contains(siteUploadFixText(n), "Save changes")
	})
	if len(saves) != 1 {
		t.Fatalf("Save changes button inside settings form = %d, want 1; page = %s", len(saves), page)
	}
	methods := siteUploadFixFind(form, func(n *xhtml.Node) bool {
		return n.Data == "input" && siteUploadFixAttr(n, "name") == "_method"
	})
	if len(methods) != 1 || siteUploadFixAttr(methods[0], "value") != "patch" {
		t.Fatalf("settings form _method inputs = %d, want single patch", len(methods))
	}
	links := siteUploadFixFind(form, func(n *xhtml.Node) bool {
		return n.Data == "a" && siteUploadFixAttr(n, "href") == uploadPath
	})
	if len(links) != 1 {
		t.Fatalf("delete links to %s = %d, want 1", uploadPath, len(links))
	}
	link := links[0]
	if got := siteUploadFixAttr(link, "data-method"); got != "delete" {
		t.Fatalf("delete link data-method = %q, want delete", got)
	}
	if got := siteUploadFixAttr(link, "class"); got != "link-button link-button--destructive" {
		t.Fatalf("delete link class = %q", got)
	}
	if !strings.Contains(siteUploadFixText(link), "Delete uploaded file") {
		t.Fatalf("delete link text = %q", siteUploadFixText(link))
	}
}

func TestAdminSettingsBrandingSiteUploadDeleteIsDataMethodLink(t *testing.T) {
	page := adminSettingsBrandingHTML(adminBrandingSettings{
		SiteTitle:         "Paon",
		ThumbnailUploadID: 5,
		ThumbnailURL:      "https://example.com/thumb.png",
	}, "", "", "en")
	assertSiteUploadSettingsFormIntact(t, page, "/admin/settings/branding", "/admin/site_uploads/5")
}

func TestAdminSettingsAppearanceSiteUploadDeleteIsDataMethodLink(t *testing.T) {
	page := adminSettingsAppearanceHTML(adminAppearanceSettings{
		Theme:          "default",
		MascotUploadID: 9,
		MascotURL:      "https://example.com/mascot.png",
	}, "", "", "en")
	assertSiteUploadSettingsFormIntact(t, page, "/admin/settings/appearance", "/admin/site_uploads/9")
}

func TestAdminSiteUploadPreviewDeleteHTMLEmptyWithoutUpload(t *testing.T) {
	if got := adminSiteUploadPreviewDeleteHTML(0, "https://example.com/a.png", "en"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := adminSiteUploadPreviewDeleteHTML(3, "", "en"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
