package layout

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// renderComponent renders a templ.Component to a string for assertions.
func renderComponent(t *testing.T, c templ.Component) string {
	t.Helper()
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

// stripStyle removes the contents of <style>...</style> so class assertions
// only check the HTML body (the CSS file is huge and changes independently).
func stripStyle(s string) string {
	for {
		start := strings.Index(s, "<style>")
		if start == -1 {
			return s
		}
		end := strings.Index(s[start:], "</style>")
		if end == -1 {
			return s
		}
		s = s[:start] + s[start+end+len("</style>"):]
	}
}

// ---------- Modal ----------

func TestModal_RendersDialogWithID(t *testing.T) {
	html := renderComponent(t, Modal(ModalProps{ID: "test-modal", Title: "Hello"}))
	if !strings.Contains(html, `<dialog id="test-modal"`) {
		t.Errorf("expected <dialog id=\"test-modal\">, got:\n%s", html)
	}
	if !strings.Contains(html, `class="ds-modal"`) {
		t.Errorf("expected class=\"ds-modal\", got:\n%s", html)
	}
	if !strings.Contains(html, `aria-modal="true"`) {
		t.Errorf("expected aria-modal=\"true\", got:\n%s", html)
	}
}

func TestModal_RendersTitleWithLabelledby(t *testing.T) {
	html := renderComponent(t, Modal(ModalProps{ID: "x", Title: "Hello"}))
	if !strings.Contains(html, `aria-labelledby="x-title"`) {
		t.Errorf("expected aria-labelledby=\"x-title\", got:\n%s", html)
	}
	if !strings.Contains(html, `id="x-title"`) {
		t.Errorf("expected id=\"x-title\" for the heading, got:\n%s", html)
	}
	if !strings.Contains(html, ">Hello<") {
		t.Errorf("expected title text 'Hello', got:\n%s", html)
	}
}

func TestModal_SizeVariants(t *testing.T) {
	cases := []struct {
		size    string
		wantCls string
	}{
		{"", ""},
		{"sm", "ds-modal--sm"},
		{"lg", "ds-modal--lg"},
		{"invalid", ""}, // unknown sizes fall back to default (no class)
	}
	for _, tc := range cases {
		t.Run(tc.size, func(t *testing.T) {
			html := renderComponent(t, Modal(ModalProps{ID: "m", Title: "T", Size: tc.size}))
			if tc.wantCls == "" {
				if strings.Contains(html, "ds-modal--") {
					t.Errorf("size %q should not add a ds-modal--* class; got:\n%s", tc.size, html)
				}
			} else {
				if !strings.Contains(html, tc.wantCls) {
					t.Errorf("size %q should add class %q; got:\n%s", tc.size, tc.wantCls, html)
				}
			}
		})
	}
}

func TestModal_RendersCloseButton(t *testing.T) {
	html := renderComponent(t, Modal(ModalProps{ID: "m", Title: "T"}))
	if !strings.Contains(html, `class="ds-modal-close"`) {
		t.Errorf("expected close button with class ds-modal-close, got:\n%s", html)
	}
	if !strings.Contains(html, `data-modal-close`) {
		t.Errorf("expected data-modal-close on close button, got:\n%s", html)
	}
	if !strings.Contains(html, `aria-label="Close"`) {
		t.Errorf("expected aria-label=\"Close\" on close button, got:\n%s", html)
	}
}

func TestModal_RendersChildren(t *testing.T) {
	child := templ.Raw("<p data-testid=\"body\">child content</p>")
	ctx := templ.WithChildren(context.Background(), child)
	c := Modal(ModalProps{ID: "m", Title: "T"})
	var buf bytes.Buffer
	if err := c.Render(ctx, &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `<p data-testid="body">child content</p>`) {
		t.Errorf("expected child content rendered, got:\n%s", html)
	}
	if !strings.Contains(html, `class="ds-modal-body"`) {
		t.Errorf("expected ds-modal-body wrapper for children, got:\n%s", html)
	}
}

// ---------- ModalTrigger ----------

func TestModalTrigger_RendersButtonWithDataAttribute(t *testing.T) {
	html := renderComponent(t, ModalTrigger("my-modal", "ds-btn--danger", "Delete"))
	if !strings.Contains(html, `<button`) {
		t.Errorf("expected <button>, got:\n%s", html)
	}
	if !strings.Contains(html, `data-modal-open="my-modal"`) {
		t.Errorf("expected data-modal-open=\"my-modal\", got:\n%s", html)
	}
	if !strings.Contains(html, ">Delete<") {
		t.Errorf("expected label 'Delete', got:\n%s", html)
	}
}

func TestModalTrigger_IncludesBaseAndExtraClass(t *testing.T) {
	html := renderComponent(t, ModalTrigger("x", "ds-btn--primary", "Open"))
	if !strings.Contains(html, `class="ds-btn ds-btn--primary"`) {
		t.Errorf("expected class=\"ds-btn ds-btn--primary\", got:\n%s", html)
	}
}

func TestModalTrigger_EmptyClassStillRendersBase(t *testing.T) {
	html := renderComponent(t, ModalTrigger("x", "", "Open"))
	if !strings.Contains(html, `class="ds-btn"`) {
		t.Errorf("expected class=\"ds-btn\", got:\n%s", html)
	}
}

// ---------- ConfirmModal ----------

func TestConfirmModal_RendersInsideDialog(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:      "confirm",
		Title:   "Delete cluster?",
		Message: "This will permanently remove cluster 'prod'.",
	}))
	if !strings.Contains(html, `<dialog id="confirm"`) {
		t.Errorf("expected <dialog id=\"confirm\">, got:\n%s", html)
	}
	if !strings.Contains(html, ">Delete cluster?<") {
		t.Errorf("expected title text, got:\n%s", html)
	}
	// templ HTML-escapes single quotes as &#39; in text content. The substring
	// 'prod' therefore renders as &#39;prod&#39; — assert the escaped form.
	if !strings.Contains(html, "This will permanently remove cluster &#39;prod&#39;.") {
		t.Errorf("expected message text (with escaped quotes), got:\n%s", html)
	}
}

func TestConfirmModal_RendersConfirmAndCancelButtons(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:           "c",
		Title:        "T",
		Message:      "M",
		ConfirmLabel: "Yes, delete",
		CancelLabel:  "No, keep",
	}))
	if !strings.Contains(html, ">Yes, delete<") {
		t.Errorf("expected confirm label 'Yes, delete', got:\n%s", html)
	}
	if !strings.Contains(html, ">No, keep<") {
		t.Errorf("expected cancel label 'No, keep', got:\n%s", html)
	}
}

func TestConfirmModal_DefaultLabels(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:      "c",
		Title:   "T",
		Message: "M",
		// ConfirmLabel and CancelLabel empty
	}))
	if !strings.Contains(html, ">Confirm<") {
		t.Errorf("expected default confirm label 'Confirm', got:\n%s", html)
	}
	if !strings.Contains(html, ">Cancel<") {
		t.Errorf("expected default cancel label 'Cancel', got:\n%s", html)
	}
}

func TestConfirmModal_CancelButtonHasDataModalClose(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:      "c",
		Title:   "T",
		Message: "M",
	}))
	if !strings.Contains(html, `data-modal-close`) {
		t.Errorf("expected data-modal-close on cancel button, got:\n%s", html)
	}
}

func TestConfirmModal_HTMXVerbPost(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:         "c",
		Title:      "T",
		Message:    "M",
		HTMXVerb:   "post",
		HTMXAction: "/api/v1/tenants",
	}))
	if !strings.Contains(html, `hx-post="/api/v1/tenants"`) {
		t.Errorf("expected hx-post=\"/api/v1/tenants\", got:\n%s", html)
	}
}

func TestConfirmModal_HTMXVerbDelete(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:         "c",
		Title:      "T",
		Message:    "M",
		HTMXVerb:   "delete",
		HTMXAction: "/api/v1/tenants/prod",
	}))
	if !strings.Contains(html, `hx-delete="/api/v1/tenants/prod"`) {
		t.Errorf("expected hx-delete attribute, got:\n%s", html)
	}
	if strings.Contains(html, `hx-post=`) {
		t.Errorf("did not expect hx-post when verb is delete, got:\n%s", html)
	}
}

func TestConfirmModal_HTMXVerbPut(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:         "c",
		Title:      "T",
		Message:    "M",
		HTMXVerb:   "put",
		HTMXAction: "/api/v1/tenants/x",
	}))
	if !strings.Contains(html, `hx-put="/api/v1/tenants/x"`) {
		t.Errorf("expected hx-put attribute, got:\n%s", html)
	}
}

func TestConfirmModal_HTMXVerbPatch(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:         "c",
		Title:      "T",
		Message:    "M",
		HTMXVerb:   "patch",
		HTMXAction: "/api/v1/tenants/x",
	}))
	if !strings.Contains(html, `hx-patch="/api/v1/tenants/x"`) {
		t.Errorf("expected hx-patch attribute, got:\n%s", html)
	}
}

func TestConfirmModal_HTMXTargetAndSwap(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:         "c",
		Title:      "T",
		Message:    "M",
		HTMXVerb:   "delete",
		HTMXAction: "/x",
		HTMXTarget: "#main",
		HTMXSwap:   "innerHTML",
	}))
	if !strings.Contains(html, `hx-target="#main"`) {
		t.Errorf("expected hx-target=\"#main\", got:\n%s", html)
	}
	if !strings.Contains(html, `hx-swap="innerHTML"`) {
		t.Errorf("expected hx-swap=\"innerHTML\", got:\n%s", html)
	}
}

func TestConfirmModal_DefaultSwap(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:         "c",
		Title:      "T",
		Message:    "M",
		HTMXVerb:   "delete",
		HTMXAction: "/x",
		// HTMXSwap empty
	}))
	if !strings.Contains(html, `hx-swap="outerHTML"`) {
		t.Errorf("expected default hx-swap=\"outerHTML\", got:\n%s", html)
	}
}

func TestConfirmModal_NoTargetOmitsAttribute(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:         "c",
		Title:      "T",
		Message:    "M",
		HTMXVerb:   "delete",
		HTMXAction: "/x",
		// HTMXTarget empty
	}))
	if strings.Contains(html, `hx-target=`) {
		t.Errorf("did not expect hx-target when empty, got:\n%s", html)
	}
}

func TestConfirmModal_ConfirmClassAppended(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:           "c",
		Title:        "T",
		Message:      "M",
		ConfirmClass: "ds-btn--danger",
		HTMXVerb:     "delete",
		HTMXAction:   "/x",
	}))
	if !strings.Contains(html, `class="ds-btn ds-btn--danger"`) {
		t.Errorf("expected class=\"ds-btn ds-btn--danger\" on confirm button, got:\n%s", html)
	}
}

func TestConfirmModal_MessageEscaped(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:      "c",
		Title:   "T",
		Message: `<script>alert("xss")</script>`,
	}))
	if strings.Contains(html, "<script>alert") {
		t.Errorf("expected message to be HTML-escaped, got:\n%s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("expected escaped &lt;script&gt;, got:\n%s", html)
	}
}

func TestConfirmModal_HTMXVerbEmpty(t *testing.T) {
	html := renderComponent(t, ConfirmModal(ConfirmModalProps{
		ID:      "c",
		Title:   "T",
		Message: "M",
		// HTMXVerb empty
	}))
	for _, attr := range []string{`hx-post=`, `hx-put=`, `hx-delete=`, `hx-patch=`} {
		if strings.Contains(html, attr) {
			t.Errorf("did not expect %q when HTMXVerb is empty, got:\n%s", attr, html)
		}
	}
}

// ---------- Layout integration ----------

func TestBase_IncludesModalScript(t *testing.T) {
	html := renderComponent(t, Base(BaseProps{
		Title:   "Test",
		Page:    "login",
		Content: templ.Raw("<p>hello</p>"),
	}))
	body := stripStyle(html)
	if !strings.Contains(body, "data-modal-open") {
		t.Errorf("expected modal opener script to reference data-modal-open, got body:\n%s", body)
	}
	if !strings.Contains(body, "data-modal-close") {
		t.Errorf("expected modal closer script to reference data-modal-close, got body:\n%s", body)
	}
}

// ---------- Tabs (#11) ----------

func TestTabs_RendersNavWithRoleTablist(t *testing.T) {
	html := renderComponent(t, Tabs(TabsProps{
		ID: "cluster-tabs",
		Items: []TabItem{
			{ID: "overview", Label: "Overview", URL: "/clusters/x"},
			{ID: "patches", Label: "Patches", URL: "/clusters/x/patches"},
		},
	}))
	if !strings.Contains(html, `<nav class="ds-tabs"`) {
		t.Errorf("expected <nav class=\"ds-tabs\">, got:\n%s", html)
	}
	if !strings.Contains(html, `role="tablist"`) {
		t.Errorf("expected role=\"tablist\", got:\n%s", html)
	}
	if !strings.Contains(html, `id="cluster-tabs"`) {
		t.Errorf("expected id=\"cluster-tabs\", got:\n%s", html)
	}
}

func TestTabs_RendersOneAnchorPerItem(t *testing.T) {
	html := renderComponent(t, Tabs(TabsProps{
		Items: []TabItem{
			{ID: "a", Label: "A", URL: "/a"},
			{ID: "b", Label: "B", URL: "/b"},
			{ID: "c", Label: "C", URL: "/c"},
		},
	}))
	count := strings.Count(html, `<a class="ds-tabs-link`)
	if count != 3 {
		t.Errorf("expected 3 <a class=\"ds-tabs-link\"> elements, got %d in:\n%s", count, html)
	}
}

func TestTabs_ActiveTabHasActiveClass(t *testing.T) {
	html := renderComponent(t, Tabs(TabsProps{
		Items: []TabItem{
			{ID: "a", Label: "A", URL: "/a"},
			{ID: "b", Label: "B", URL: "/b", Active: true},
			{ID: "c", Label: "C", URL: "/c"},
		},
	}))
	count := strings.Count(html, "ds-tabs-link--active")
	if count != 1 {
		t.Errorf("expected exactly 1 ds-tabs-link--active, got %d in:\n%s", count, html)
	}
	if !strings.Contains(html, `id="tabs-tab-b"`) {
		t.Errorf("expected id=\"tabs-tab-b\" for tab b, got:\n%s", html)
	}
	if strings.Count(html, `aria-selected="true"`) != 1 {
		t.Errorf("expected exactly 1 aria-selected=\"true\", got %d in:\n%s",
			strings.Count(html, `aria-selected="true"`), html)
	}
}

func TestTabs_InactiveTabsAriaSelectedFalse(t *testing.T) {
	html := renderComponent(t, Tabs(TabsProps{
		Items: []TabItem{
			{ID: "a", Label: "A", URL: "/a"},
			{ID: "b", Label: "B", URL: "/b"},
		},
	}))
	if strings.Count(html, `aria-selected="false"`) != 2 {
		t.Errorf("expected 2 aria-selected=\"false\", got %d in:\n%s",
			strings.Count(html, `aria-selected="false"`), html)
	}
}

func TestTabs_TabLinksPointToURL(t *testing.T) {
	html := renderComponent(t, Tabs(TabsProps{
		Items: []TabItem{
			{ID: "a", Label: "A", URL: "/clusters/x/patches"},
			{ID: "b", Label: "B", URL: "/clusters/x/backups"},
		},
	}))
	if !strings.Contains(html, `href="/clusters/x/patches"`) {
		t.Errorf("expected href=\"/clusters/x/patches\", got:\n%s", html)
	}
	if !strings.Contains(html, `href="/clusters/x/backups"`) {
		t.Errorf("expected href=\"/clusters/x/backups\", got:\n%s", html)
	}
}

func TestTabs_EmptyItemsNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Tabs with empty Items panicked: %v", r)
		}
	}()
	html := renderComponent(t, Tabs(TabsProps{Items: nil}))
	if !strings.Contains(html, `<nav class="ds-tabs"`) {
		t.Errorf("expected empty tabs to still render the nav shell, got:\n%s", html)
	}
	if strings.Contains(html, "ds-tabs-link") {
		t.Errorf("expected no tab links for empty Items, got:\n%s", html)
	}
}

func TestTabs_DefaultIDWhenEmpty(t *testing.T) {
	html := renderComponent(t, Tabs(TabsProps{
		// ID empty
		Items: []TabItem{{ID: "a", Label: "A", URL: "/a"}},
	}))
	if !strings.Contains(html, `id="tabs"`) {
		t.Errorf("expected default id=\"tabs\" when ID empty, got:\n%s", html)
	}
	if !strings.Contains(html, `id="tabs-tab-a"`) {
		t.Errorf("expected derived id=\"tabs-tab-a\", got:\n%s", html)
	}
}

func TestTabs_CustomIDPropagatesToTabIDs(t *testing.T) {
	html := renderComponent(t, Tabs(TabsProps{
		ID:    "my-tabs",
		Items: []TabItem{{ID: "x", Label: "X", URL: "/x"}},
	}))
	if !strings.Contains(html, `id="my-tabs"`) {
		t.Errorf("expected id=\"my-tabs\", got:\n%s", html)
	}
	if !strings.Contains(html, `id="my-tabs-tab-x"`) {
		t.Errorf("expected derived id=\"my-tabs-tab-x\", got:\n%s", html)
	}
}

// ---------- CopyButton (#12) ----------

func TestCopyButton_RendersDataCopyText(t *testing.T) {
	html := renderComponent(t, CopyButton(CopyButtonProps{Text: "abc-123"}))
	if !strings.Contains(html, `data-copy-text="abc-123"`) {
		t.Errorf("expected data-copy-text=\"abc-123\", got:\n%s", html)
	}
}

func TestCopyButton_DataCopyTextEscaped(t *testing.T) {
	// templ HTML-attribute-escapes the value, so quotes are escaped.
	html := renderComponent(t, CopyButton(CopyButtonProps{Text: `value with "quotes"`}))
	if !strings.Contains(html, `data-copy-text="value with &#34;quotes&#34;"`) {
		t.Errorf("expected data-copy-text value to be escaped, got:\n%s", html)
	}
}

func TestCopyButton_DefaultLabelIsCopy(t *testing.T) {
	html := renderComponent(t, CopyButton(CopyButtonProps{Text: "x", Label: ""}))
	if !strings.Contains(html, ">Copy<") {
		t.Errorf("expected default label 'Copy', got:\n%s", html)
	}
}

func TestCopyButton_CustomLabel(t *testing.T) {
	html := renderComponent(t, CopyButton(CopyButtonProps{Text: "x", Label: "Copy token"}))
	if !strings.Contains(html, ">Copy token<") {
		t.Errorf("expected label 'Copy token', got:\n%s", html)
	}
}

func TestCopyButton_HasCopyBtnClass(t *testing.T) {
	html := renderComponent(t, CopyButton(CopyButtonProps{Text: "x"}))
	if !strings.Contains(html, `ds-copy-btn`) {
		t.Errorf("expected class ds-copy-btn, got:\n%s", html)
	}
	if !strings.Contains(html, `ds-btn`) {
		t.Errorf("expected base class ds-btn, got:\n%s", html)
	}
}

func TestCopyButton_ExtraClassAppended(t *testing.T) {
	html := renderComponent(t, CopyButton(CopyButtonProps{Text: "x", Class: "ds-btn--sm"}))
	if !strings.Contains(html, `ds-btn--sm`) {
		t.Errorf("expected extra class ds-btn--sm, got:\n%s", html)
	}
}

func TestCopyButton_EmptyExtraClassNoTrailingSpace(t *testing.T) {
	html := renderComponent(t, CopyButton(CopyButtonProps{Text: "x", Class: ""}))
	// Should NOT have trailing space inside class attribute
	if strings.Contains(html, `class="ds-btn ds-copy-btn "`) {
		t.Errorf("expected no trailing space in class, got:\n%s", html)
	}
	if !strings.Contains(html, `class="ds-btn ds-copy-btn"`) {
		t.Errorf("expected class=\"ds-btn ds-copy-btn\", got:\n%s", html)
	}
}

func TestCopyButton_IconOnlyAddsAriaLabel(t *testing.T) {
	html := renderComponent(t, CopyButton(CopyButtonProps{Text: "x", IconOnly: true}))
	if !strings.Contains(html, `aria-label="Copy to clipboard"`) {
		t.Errorf("expected aria-label=\"Copy to clipboard\" when IconOnly, got:\n%s", html)
	}
}

func TestCopyButton_NotIconOnlyOmitsAriaLabel(t *testing.T) {
	html := renderComponent(t, CopyButton(CopyButtonProps{Text: "x", IconOnly: false}))
	if strings.Contains(html, `aria-label="Copy to clipboard"`) {
		t.Errorf("did not expect aria-label when not IconOnly, got:\n%s", html)
	}
}

func TestCopyButton_IncludesClipboardIcon(t *testing.T) {
	html := renderComponent(t, CopyButton(CopyButtonProps{Text: "x"}))
	if !strings.Contains(html, `<svg class="ds-copy-btn-icon"`) {
		t.Errorf("expected clipboard icon SVG, got:\n%s", html)
	}
}

func TestCopyButton_HasAlpineState(t *testing.T) {
	html := renderComponent(t, CopyButton(CopyButtonProps{Text: "x"}))
	if !strings.Contains(html, `x-data="{ copied: false }"`) {
		t.Errorf("expected Alpine.js x-data state, got:\n%s", html)
	}
	if !strings.Contains(html, `x-on:click=`) {
		t.Errorf("expected x-on:click handler, got:\n%s", html)
	}
}

// ---------- CodeBlock (#12) ----------

func TestCodeBlock_RendersFigure(t *testing.T) {
	html := renderComponent(t, CodeBlock(CodeBlockProps{Code: "hello"}))
	if !strings.Contains(html, `<figure class="ds-codeblock"`) {
		t.Errorf("expected <figure class=\"ds-codeblock\">, got:\n%s", html)
	}
}

func TestCodeBlock_RendersPreCode(t *testing.T) {
	html := renderComponent(t, CodeBlock(CodeBlockProps{Code: "hello"}))
	if !strings.Contains(html, `<pre class="ds-codeblock-pre"`) {
		t.Errorf("expected <pre class=\"ds-codeblock-pre\">, got:\n%s", html)
	}
	if !strings.Contains(html, `<code>hello</code>`) {
		t.Errorf("expected <code>hello</code>, got:\n%s", html)
	}
}

func TestCodeBlock_EscapesHTMLInCode(t *testing.T) {
	// XSS check: angle brackets and ampersands must be escaped in code body.
	html := renderComponent(t, CodeBlock(CodeBlockProps{
		Code: `<script>alert("xss")</script> & 'foo'`,
	}))
	if strings.Contains(html, "<script>alert") {
		t.Errorf("expected code body to be HTML-escaped, got:\n%s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("expected escaped &lt;script&gt;, got:\n%s", html)
	}
	if !strings.Contains(html, "&amp;") {
		t.Errorf("expected escaped &amp; for &, got:\n%s", html)
	}
}

func TestCodeBlock_EscapesQuotesInCode(t *testing.T) {
	html := renderComponent(t, CodeBlock(CodeBlockProps{
		Code: `"hello"`,
	}))
	if !strings.Contains(html, "&#34;hello&#34;") {
		t.Errorf("expected escaped quotes, got:\n%s", html)
	}
}

func TestCodeBlock_LanguageAddedAsClass(t *testing.T) {
	html := renderComponent(t, CodeBlock(CodeBlockProps{
		Language: "yaml",
		Code:     "key: value",
	}))
	if !strings.Contains(html, `class="language-yaml"`) {
		t.Errorf("expected class=\"language-yaml\", got:\n%s", html)
	}
}

func TestCodeBlock_EmptyLanguageOmitsClass(t *testing.T) {
	html := renderComponent(t, CodeBlock(CodeBlockProps{
		Code: "x",
	}))
	if strings.Contains(html, `class="language-"`) {
		t.Errorf("did not expect language- class when Language empty, got:\n%s", html)
	}
	if !strings.Contains(html, `<code>x</code>`) {
		t.Errorf("expected plain <code>x</code>, got:\n%s", html)
	}
}

func TestCodeBlock_EmptyCodeRendersEmptyCodeTag(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CodeBlock with empty Code panicked: %v", r)
		}
	}()
	html := renderComponent(t, CodeBlock(CodeBlockProps{Code: ""}))
	if !strings.Contains(html, "<code></code>") {
		t.Errorf("expected <code></code> for empty Code, got:\n%s", html)
	}
}

func TestCodeBlock_IDAttribute(t *testing.T) {
	html := renderComponent(t, CodeBlock(CodeBlockProps{ID: "my-code", Code: "x"}))
	if !strings.Contains(html, `id="my-code"`) {
		t.Errorf("expected id=\"my-code\", got:\n%s", html)
	}
}

func TestCodeBlock_EmptyIDOmitsIDAttribute(t *testing.T) {
	html := renderComponent(t, CodeBlock(CodeBlockProps{Code: "x"}))
	if strings.Contains(html, `id=`) {
		t.Errorf("did not expect id attribute when ID empty, got:\n%s", html)
	}
}

func TestCodeBlock_CaptionRenderedInHeader(t *testing.T) {
	html := renderComponent(t, CodeBlock(CodeBlockProps{
		Caption: "/etc/talos/config.yaml",
		Code:    "x",
	}))
	if !strings.Contains(html, `<figcaption class="ds-codeblock-caption">`) {
		t.Errorf("expected <figcaption class=\"ds-codeblock-caption\">, got:\n%s", html)
	}
	if !strings.Contains(html, "/etc/talos/config.yaml") {
		t.Errorf("expected caption text, got:\n%s", html)
	}
}

func TestCodeBlock_NoCaptionOrCopyOmitsHeader(t *testing.T) {
	html := renderComponent(t, CodeBlock(CodeBlockProps{Code: "x"}))
	if strings.Contains(html, `class="ds-codeblock-header"`) {
		t.Errorf("did not expect ds-codeblock-header when no caption and no copy, got:\n%s", html)
	}
}

func TestCodeBlock_ShowCopyRendersCopyButton(t *testing.T) {
	html := renderComponent(t, CodeBlock(CodeBlockProps{
		Code:     "kubectl get nodes",
		ShowCopy: true,
	}))
	if !strings.Contains(html, `class="ds-codeblock-header"`) {
		t.Errorf("expected ds-codeblock-header when ShowCopy, got:\n%s", html)
	}
	if !strings.Contains(html, `ds-copy-btn`) {
		t.Errorf("expected CopyButton rendered when ShowCopy, got:\n%s", html)
	}
	if !strings.Contains(html, `data-copy-text="kubectl get nodes"`) {
		t.Errorf("expected CopyButton data-copy-text to match Code, got:\n%s", html)
	}
}

func TestCodeBlock_MaxLinesAddsMaxHeightStyle(t *testing.T) {
	html := renderComponent(t, CodeBlock(CodeBlockProps{
		Code:     "x",
		MaxLines: 30,
	}))
	if !strings.Contains(html, `max-height:`) {
		t.Errorf("expected max-height style when MaxLines > 0, got:\n%s", html)
	}
	if !strings.Contains(html, `30`) {
		t.Errorf("expected style to reference MaxLines (30), got:\n%s", html)
	}
	if !strings.Contains(html, `overflow: auto`) {
		t.Errorf("expected overflow: auto when MaxLines > 0, got:\n%s", html)
	}
}

func TestCodeBlock_NoMaxLinesOmitsStyle(t *testing.T) {
	html := renderComponent(t, CodeBlock(CodeBlockProps{Code: "x"}))
	// Style attribute may be empty string or absent; either is fine.
	// Just verify it does not contain "max-height".
	if strings.Contains(html, `max-height:`) {
		t.Errorf("did not expect max-height when MaxLines is 0, got:\n%s", html)
	}
}

func TestCodeBlock_CompleteRender(t *testing.T) {
	// Full integration: all fields set.
	html := renderComponent(t, CodeBlock(CodeBlockProps{
		ID:       "talos-config",
		Language: "yaml",
		Code:     "machine:\n  type: controlplane",
		MaxLines: 40,
		ShowCopy: true,
		Caption:  "/etc/talos/config.yaml",
	}))
	if !strings.Contains(html, `id="talos-config"`) {
		t.Errorf("missing id, got:\n%s", html)
	}
	if !strings.Contains(html, `class="language-yaml"`) {
		t.Errorf("missing language class, got:\n%s", html)
	}
	if !strings.Contains(html, `data-copy-text="machine:`) {
		t.Errorf("missing copy button with code, got:\n%s", html)
	}
	if !strings.Contains(html, "machine:") {
		t.Errorf("missing code body, got:\n%s", html)
	}
	if !strings.Contains(html, "/etc/talos/config.yaml") {
		t.Errorf("missing caption, got:\n%s", html)
	}
}

// ---------- Theme support (#67) ----------

// TestCSS_HasSemanticTokens confirms the CSS defines the semantic indirection
// layer that the dark theme remap targets. Without these tokens, :root.dark
// has nothing to override.
func TestCSS_HasSemanticTokens(t *testing.T) {
	css := CSS()
	for _, token := range []string{
		"--bg:",
		"--bg-elevated:",
		"--bg-elevated-strong:",
		"--fg:",
		"--fg-muted:",
		"--border:",
		"--accent:",
		"--success:",
		"--danger:",
		"--warning:",
		"--overlay:",
		"--fg-on-accent:",
		"--fg-on-danger:",
	} {
		if !strings.Contains(css, token) {
			t.Errorf("CSS() missing semantic token %s", token)
		}
	}
}

// TestCSS_HasDarkModeRemap confirms :root.dark exists and remaps the
// semantic tokens to the NeXT palette.
func TestCSS_HasDarkModeRemap(t *testing.T) {
	css := CSS()
	if !strings.Contains(css, ":root.dark {") {
		t.Fatalf("CSS() missing :root.dark block — dark mode will not activate")
	}
	// Every semantic token defined in :root must be remapped in :root.dark.
	// We find the dark block and assert it contains the key remaps.
	idx := strings.Index(css, ":root.dark {")
	end := strings.Index(css[idx:], "}")
	if end == -1 {
		t.Fatalf("could not find closing brace of :root.dark block")
	}
	darkBlock := css[idx : idx+end]
	for _, remap := range []string{
		"--bg: var(--next-black)",
		"--fg: var(--next-white)",
		"--accent: var(--next-teal)",
		"--border: var(--next-mid)",
		"--success: var(--positive-next)",
		"--danger: var(--negative-next)",
		"color-scheme: dark",
	} {
		if !strings.Contains(darkBlock, remap) {
			t.Errorf("CSS() :root.dark block missing remap %q", remap)
		}
	}
}

// TestCSS_LightModeSetsColorScheme confirms the light :root block announces
// color-scheme: light so native form controls render in the right palette.
func TestCSS_LightModeSetsColorScheme(t *testing.T) {
	css := CSS()
	// Find the first :root block (not :root.dark)
	idx := strings.Index(css, ":root {")
	if idx == -1 {
		t.Fatalf("CSS() missing :root block")
	}
	end := strings.Index(css[idx:], "}")
	rootBlock := css[idx : idx+end]
	if !strings.Contains(rootBlock, "color-scheme: light") {
		t.Errorf("CSS() :root block missing 'color-scheme: light' — native controls will use UA default")
	}
}

// TestCSS_ComponentRulesUseSemanticTokens is the regression guard: every
// .ds-* rule must reference semantic tokens, never the raw palette tokens
// directly. Raw palette refs bypass the dark-mode remap.
func TestCSS_ComponentRulesUseSemanticTokens(t *testing.T) {
	css := CSS()

	// Locate the boundary between token declarations and component rules.
	// The component rules start at the global body block comment.
	boundary := strings.Index(css, "Global body / form defaults")
	if boundary == -1 {
		t.Fatalf("could not locate component-rule boundary — CSS structure changed")
	}
	componentCSS := css[boundary:]

	// Raw tokens that components must never reference directly. They must
	// go through the semantic indirection.
	forbidden := []string{
		"var(--paper)",
		"var(--surface)",
		"var(--surface-strong)",
		"var(--ink)",
		"var(--ink-muted)",
		"var(--rule)",
		"var(--positive)",
		"var(--negative)",
	}
	for _, raw := range forbidden {
		if strings.Contains(componentCSS, raw) {
			// Show context for easier debugging
			idx := strings.Index(componentCSS, raw)
			ctxStart := idx - 60
			if ctxStart < 0 {
				ctxStart = 0
			}
			ctxEnd := idx + len(raw) + 60
			if ctxEnd > len(componentCSS) {
				ctxEnd = len(componentCSS)
			}
			t.Errorf("component CSS must not reference raw palette token %s directly (use the semantic equivalent). Context: %q",
				raw, componentCSS[ctxStart:ctxEnd])
		}
	}
}

// TestCSS_NoHardCodedColorsInComponentRules guards against hex / rgb / rgba /
// oklch literals in component CSS — they bypass the theme system entirely.
//
// Token declarations in :root are allowed to use oklch() (they're the source
// of truth). Anything below the boundary must go through var(--...).
func TestCSS_NoHardCodedColorsInComponentRules(t *testing.T) {
	css := CSS()
	boundary := strings.Index(css, "Global body / form defaults")
	if boundary == -1 {
		t.Fatalf("could not locate component-rule boundary")
	}
	componentCSS := css[boundary:]

	// Hex colors (#abc, #abcdef, #abcdef99). Tolerate # in CSS selectors
	// (there shouldn't be any in this file, but defensive).
	hexMatches := regexFindAll(hexColorPattern, componentCSS)
	if len(hexMatches) > 0 {
		t.Errorf("component CSS contains %d hard-coded hex color(s); first = %q. Use semantic tokens instead.",
			len(hexMatches), hexMatches[0])
	}

	// rgb(...) / rgba(...) — none in components.
	for _, fn := range []string{"rgb(", "rgba("} {
		if strings.Contains(componentCSS, fn) {
			idx := strings.Index(componentCSS, fn)
			t.Errorf("component CSS contains hard-coded %s... — use var(--...) instead. Context: %q",
				fn, componentCSS[imax(0, idx-40):idx+40])
		}
	}

	// oklch(...) — allowed only inside var() declarations or comments. Since
	// we already banned raw palette tokens above, any oklch() in component
	// rules is by definition hard-coded. Allow the special case of oklch()
	// inside the overlay (used for backdrop fade — would be unusual to find
	// in component CSS).
	oklchMatches := regexFindAll(oklchPattern, componentCSS)
	if len(oklchMatches) > 0 {
		t.Errorf("component CSS contains %d hard-coded oklch() color(s); first = %q. Use semantic tokens instead.",
			len(oklchMatches), oklchMatches[0])
	}
}

// TestBase_IncludesPrePaintThemeScript verifies the inline script in <head>
// applies the .dark class before first paint. Without it, dark-mode users
// see a flash of light theme on every page load (FOUC).
func TestBase_IncludesPrePaintThemeScript(t *testing.T) {
	html := renderComponent(t, Base(BaseProps{
		Title:   "Test",
		Page:    "login",
		Content: templ.Raw("<p>hello</p>"),
	}))
	// The script must be in <head>, so it runs before <body> paints.
	headEnd := strings.Index(html, "</head>")
	if headEnd == -1 {
		t.Fatalf("could not locate </head>")
	}
	head := html[:headEnd]
	for _, want := range []string{
		"localStorage.getItem('rezuscloud-theme')",
		"matchMedia('(prefers-color-scheme: dark)')",
		"document.documentElement.classList.add('dark')",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("pre-paint theme script in <head> missing %q", want)
		}
	}
}

// TestBase_RendersThemeToggleButton confirms the topbar (right side,
// alongside the breadcrumb) renders the Mac/NeXT toggle with sun/moon
// icons. The toggle must be in the topbar — not buried in the sidebar
// footer — so users can find it without scrolling.
func TestBase_RendersThemeToggleButton(t *testing.T) {
	html := renderComponent(t, Base(BaseProps{
		Title:   "Dashboard",
		Page:    "dashboard",
		User:    "admin",
		Content: templ.Raw("<p>main</p>"),
	}))
	body := stripStyle(html)
	if !strings.Contains(body, `class="ds-theme-toggle"`) {
		t.Errorf("expected topbar to include a ds-theme-toggle button, got body:\n%s", body)
	}
	if !strings.Contains(body, `aria-label="Toggle theme"`) {
		t.Errorf("expected aria-label on theme toggle, got body:\n%s", body)
	}
	if !strings.Contains(body, `rezuscloud-theme`) {
		t.Errorf("expected toggle to write to localStorage key 'rezuscloud-theme', got body:\n%s", body)
	}
	// Regression guard for #69: bare `x-data` (no value) silently kills the
	// Alpine component — click handler never fires. Must have a value.
	if !strings.Contains(body, `x-data="{`) {
		t.Errorf("expected x-data=\"{...}\" (Alpine v3 requires an expression — bare x-data kills the component, see #69), got body:\n%s", body)
	}
	if strings.Contains(body, `x-data x-init`) || strings.Contains(body, `>x-data<`) {
		t.Errorf("detected bare 'x-data' (no value) — Alpine v3 will not initialize, see #69")
	}
	// Sun icon: shown when dark mode is active (clicking switches to light)
	if !strings.Contains(body, `ds-theme-toggle-icon`) {
		t.Errorf("expected toggle to render ds-theme-toggle-icon SVG(s), got body:\n%s", body)
	}
	// Both sun and moon icons must be present (Alpine controls visibility)
	sunPath := `<circle cx="12" cy="12" r="4"`
	moonPath := `M20.354 15.354A9 9 0 018.646 3.646`
	if !strings.Contains(body, sunPath) {
		t.Errorf("expected sun icon (circle + rays) — `%s` — to be in HTML, got body:\n%s", sunPath, body)
	}
	if !strings.Contains(body, moonPath) {
		t.Errorf("expected moon icon (crescent path containing `%s`) to be in HTML, got body:\n%s", moonPath, body)
	}
	// x-cloak on one of the icons (the dark-mode-only one) so it doesn't
	// flash before Alpine initializes
	if !strings.Contains(body, `x-cloak`) {
		t.Errorf("expected x-cloak on the dark-mode-only SVG to prevent FOUC, got body:\n%s", body)
	}
	// No text content between the button tags — it's icon-only
	openIdx := strings.Index(body, `class="ds-theme-toggle"`)
	if openIdx == -1 {
		t.Fatalf("theme toggle not found")
	}
	closeIdx := strings.Index(body[openIdx:], `</button>`)
	if closeIdx == -1 {
		t.Fatalf("theme toggle button not closed")
	}
	btnContents := body[openIdx : openIdx+closeIdx]
	for _, forbidden := range []string{"Mac", "NeXT"} {
		if strings.Contains(btnContents, ">"+forbidden+"<") {
			t.Errorf("theme toggle must be icon-only, found text %q inside button: %s", forbidden, btnContents)
		}
	}
	// Toggle must be in the topbar, not the sidebar footer.
	if !strings.Contains(body, `class="ds-topbar"`) {
		t.Errorf("expected topbar wrapper .ds-topbar to be rendered (toggle must live in the topbar, not the sidebar footer), got body:\n%s", body)
	}
}

// TestBase_LoginPageHasFloatingThemeToggle confirms the login page (which
// has no sidebar / topbar) still renders the toggle as a fixed-position
// element in the top-right corner. Users should be able to flip theme
// before they even log in.
func TestBase_LoginPageHasFloatingThemeToggle(t *testing.T) {
	html := renderComponent(t, Base(BaseProps{
		Title:   "Login",
		Page:    "login",
		Content: templ.Raw("<p>form</p>"),
	}))
	body := stripStyle(html)
	// Login page must include the toggle button itself.
	if !strings.Contains(body, `class="ds-theme-toggle"`) {
		t.Errorf("expected login page to render the theme toggle (even without sidebar), got body:\n%s", body)
	}
	// And it must be wrapped in the fixed-position container so it appears
	// in the top-right corner rather than inline with the form.
	if !strings.Contains(body, `class="ds-floating-top-right"`) {
		t.Errorf("expected login page to wrap the toggle in .ds-floating-top-right for fixed top-right placement, got body:\n%s", body)
	}
	// And it must NOT include the topbar (no breadcrumb on login).
	if strings.Contains(body, `class="ds-topbar"`) {
		t.Errorf("login page should not render .ds-topbar (no breadcrumb), got body:\n%s", body)
	}
	// And it must NOT include the sidebar.
	if strings.Contains(body, `class="ds-sidebar"`) {
		t.Errorf("login page should not render the sidebar, got body:\n%s", body)
	}
}

// TestBase_LoginPageOmitsThemeToggle has been replaced by the inverted
// assertion TestBase_LoginPageHasFloatingThemeToggle (login now does
// render the toggle, just in fixed position).

// ---------- Test helpers ----------

const (
	hexColorPattern = `#[0-9a-fA-F]{3,8}\b`
	oklchPattern    = `oklch\([^)]+\)`
)

// regexFindAll is a thin wrapper around regexp.FindAllString. Kept here so
// the test file doesn't need to import regexp at the top level — the helper
// is only used by the theme tests.
func regexFindAll(pattern, src string) []string {
	return regexp.MustCompile(pattern).FindAllString(src, -1)
}

func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
}
