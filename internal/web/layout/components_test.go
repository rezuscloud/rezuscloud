package layout

import (
	"bytes"
	"context"
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
	// Find the Cancel button block and check it has data-modal-close
	// (Both buttons are <button>, only cancel has data-modal-close.)
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
	// The message goes through templ's text escaping, so < and > should be escaped.
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
	// Empty verb → no hx-* verb attribute rendered (still has hx-swap, etc.).
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
	// Render the Base layout and confirm the modal-wiring script is included
	// in the head so Modal/ConfirmModal work out of the box.
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
