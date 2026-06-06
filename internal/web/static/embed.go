// Package static bundles WebUI static assets (JS SSE consumers) so they ship
// inside the binary and work in Docker / Helm deployments where the source
// tree is not available on disk.
package static

import "embed"

//go:embed logs-stream.js monitor-stream.js
var Files embed.FS
