package scaffold

import (
	_ "embed"
	"strings"
)

//go:embed widget_version.txt
var widgetVersionTxt string

// WidgetVersion returns the @blocks-network/embed-auth version string
// embedded at compile time from widget_version.txt.
// Update widget_version.txt when the widget package version bumps.
func WidgetVersion() string {
	return strings.TrimSpace(widgetVersionTxt)
}
