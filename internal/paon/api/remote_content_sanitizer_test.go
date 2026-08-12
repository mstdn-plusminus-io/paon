package api

import (
	"strings"
	"testing"
)

func TestSanitizeRemoteNoteContentStripsDangerousMarkup(t *testing.T) {
	in := `<p>hello <script>alert(1)</script><a href="https://x.example" onclick="evil()">link</a></p>`
	out := sanitizeRemoteNoteContent(in)
	if strings.Contains(out, "<script") {
		t.Fatalf("script tag must be stripped, got %q", out)
	}
	if strings.Contains(out, "onclick") {
		t.Fatalf("event handler attributes must be stripped, got %q", out)
	}
	if !strings.Contains(out, `href="https://x.example"`) {
		t.Fatalf("safe link href must be preserved, got %q", out)
	}
	if !strings.Contains(out, "link") {
		t.Fatalf("link text must be preserved, got %q", out)
	}
}

func TestSanitizeRemoteNoteContentPreservesAllowedTags(t *testing.T) {
	in := `<p>para</p><br><strong>bold</strong><a href="https://x.example" rel="tag">x</a>`
	out := sanitizeRemoteNoteContent(in)
	for _, want := range []string{"<p>para</p>", "<br", "<strong>bold</strong>", `href="https://x.example"`, `rel="nofollow noopener noreferrer"`, `target="_blank"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("allowed markup %q missing from %q", want, out)
		}
	}
	if strings.Contains(out, `rel="tag"`) {
		t.Fatalf("remote link rel should be normalized like Rails MASTODON_STRICT, got %q", out)
	}
}

func TestSanitizeRemoteNoteContentAppliesRailsClassAndElementAllowlist(t *testing.T) {
	in := `<p><span class="invisible bad p-name e-content">x</span><img src="https://x.example/emoji.png" alt=":x:"><a href="https://x.example" class="mention bad u-url" translate="no" title="x">link</a><div>block</div></p>`
	out := sanitizeRemoteNoteContent(in)
	for _, want := range []string{
		`<span class="invisible p-name e-content">x</span>`,
		`<a href="https://x.example" class="mention u-url" translate="no" rel="nofollow noopener noreferrer" target="_blank">link</a>`,
		`block`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("remote note sanitizer missing %q from %q", want, out)
		}
	}
	for _, unwanted := range []string{`<img`, ` bad`, `title=`, `<div`} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("remote note sanitizer kept %q in %q", unwanted, out)
		}
	}
}

func TestSanitizeRemoteNoteContentEmpty(t *testing.T) {
	if got := sanitizeRemoteNoteContent(""); got != "" {
		t.Fatalf("empty content = %q", got)
	}
	if got := sanitizeRemoteNoteContent("   "); got != "   " {
		t.Fatalf("blank content = %q", got)
	}
}

func TestSanitizeRemoteNoteContentDropsMalformedMathAnnotationsWithoutPanicking(t *testing.T) {
	for _, input := range []string{
		`<math><semantics><annotation>x</annotation></semantics></math>`,
		`<math><semantics><annotation class="foo">x</annotation></semantics></math>`,
	} {
		output := sanitizeRemoteNoteContent(input)
		if strings.Contains(output, "<math") || strings.Contains(output, "<annotation") || strings.Contains(output, ">x<") {
			t.Fatalf("malformed MathML annotation survived strict sanitization: %q", output)
		}
	}
}

func TestSanitizeRemoteNoteContentUsesMastodon44MathFallbacks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "inline tex",
			in:   `<math><semantics><mrow><mi>x</mi></mrow><annotation encoding="application/x-tex">x^n+y</annotation></semantics></math>`,
			want: `$x^n+y$`,
		},
		{
			name: "block tex",
			in:   `<math display="block"><semantics><mrow><mi>x</mi></mrow><annotation encoding="application/x-tex">x^n+y</annotation></semantics></math>`,
			want: `$$x^n+y$$`,
		},
		{
			name: "plain text",
			in:   `<math><semantics><msqrt><mi>x</mi></msqrt><annotation encoding="text/plain">sqrt(x)</annotation></semantics></math>`,
			want: `sqrt(x)`,
		},
		{
			name: "tex preferred",
			in:   `<math><semantics><annotation encoding="text/plain">sqrt(x)</annotation><annotation encoding="application/x-tex">\sqrt x</annotation></semantics></math>`,
			want: `$\sqrt x$`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sanitizeRemoteNoteContent(test.in); got != test.want {
				t.Fatalf("sanitizeRemoteNoteContent() = %q, want %q", got, test.want)
			}
		})
	}
}
