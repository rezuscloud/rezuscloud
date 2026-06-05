package pages

// phaseBadgeClass maps a tenant phase to a design system status badge variant.
func phaseBadgeClass(phase string) string {
	switch phase {
	case "active":
		return "positive"
	case "removing":
		return "negative"
	case "forming", "shrinking":
		return "neutral"
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
