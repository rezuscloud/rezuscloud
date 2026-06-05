// Package layout provides the base HTML layout for the WebUI.
package layout

import (
	"embed"

	"github.com/a-h/templ"
)

//go:embed webui.css
var cssFS embed.FS

// BaseProps controls what the layout renders.
type BaseProps struct {
	Title      string
	Page       string // active nav item: "dashboard", "tenants", "tenant", "login"
	User       string // empty if not logged in
	Content    templ.Component
	Breadcrumb []BreadcrumbItem // optional, prepended with Home
	Toast      ToastData        // optional flash message
}

// CSS returns the inline CSS for the WebUI, following the RezusCloud Design System.
func CSS() string {
	data, err := cssFS.ReadFile("webui.css")
	if err != nil {
		return ""
	}
	return string(data)
}
