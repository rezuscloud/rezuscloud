package pages

import (
	"fmt"
	"math"
)

// phaseBadgeClass maps a tenant phase to a design system status badge variant.
func phaseBadgeClass(phase string) string {
	switch phase {
	case "active":
		return "positive"
	case "removing":
		return "negative"
	case "forming", "shrinking", "upgrading", "precheck", "idle", "canceled":
		return "neutral"
	case "complete":
		return "positive"
	case "failed":
		return "negative"
	default:
		return "neutral"
	}
}

// reconcileBadgeClass maps a reconciliation phase to a badge variant.
func reconcileBadgeClass(phase string) string {
	switch phase {
	case "applied":
		return "positive"
	case "failed":
		return "negative"
	case "queued", "applying":
		return "warning"
	default:
		return "neutral"
	}
}

// stageBadgeClass maps a machine stage to a design system status badge variant.
func stageBadgeClass(stage string) string {
	switch stage {
	case "ready":
		return "positive"
	case "off", "removing", "stopping":
		return "negative"
	default:
		return "neutral"
	}
}

// pressureClass returns a CSS class suffix for a pressure percentage.
// ok < 70%, warning 70-89%, critical >= 90%.
func pressureClass(pct int) string {
	switch {
	case pct >= 90:
		return "critical"
	case pct >= 70:
		return "warning"
	default:
		return "ok"
	}
}

// nodeStatusClass maps a node status to a design system status dot variant.
func nodeStatusClass(status string) string {
	switch status {
	case "healthy":
		return "positive"
	case "warning":
		return "warning"
	default:
		return "negative"
	}
}

// pctInt returns usage as a percentage of capacity (0-100).
func pctInt(usage, capacity int64) int {
	if capacity <= 0 {
		return 0
	}
	p := int(usage * 100 / capacity)
	if p > 100 {
		p = 100
	}
	return p
}

// formatMillicores converts millicores to a human-readable string.
func formatMillicores(m int64) string {
	cores := float64(m) / 1000.0
	if cores >= 1 {
		return fmt.Sprintf("%.1f cores", cores)
	}
	return fmt.Sprintf("%dm", m)
}

// formatBytes converts bytes to a human-readable string (GiB, MiB, KiB).
func formatBytes(b int64) string {
	const (
		giB = 1 << 30
		miB = 1 << 20
		kiB = 1 << 10
	)
	switch {
	case b >= giB:
		return fmt.Sprintf("%.1f GiB", math.Round(float64(b)*100/float64(giB))/100)
	case b >= miB:
		return fmt.Sprintf("%.0f MiB", math.Round(float64(b)/float64(miB)))
	case b >= kiB:
		return fmt.Sprintf("%.0f KiB", math.Round(float64(b)/float64(kiB)))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
