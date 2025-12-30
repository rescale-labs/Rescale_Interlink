// Package notify provides cross-platform desktop notifications for Rescale Interlink.
// It uses github.com/gen2brain/beeep for cross-platform notification support.
package notify

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gen2brain/beeep"
	"github.com/rescale/rescale-int/internal/logging"
)

// Notifier handles desktop notifications.
type Notifier struct {
	logger  *logging.Logger
	enabled bool
	mu      sync.RWMutex
}

// Config holds notification configuration.
type Config struct {
	// Enabled determines if notifications are sent.
	Enabled bool

	// ShowDownloadComplete shows notifications for successful downloads.
	ShowDownloadComplete bool

	// ShowDownloadFailed shows notifications for failed downloads.
	ShowDownloadFailed bool

	// ShowServiceStatus shows notifications for service state changes.
	ShowServiceStatus bool
}

// DefaultConfig returns the default notification configuration.
func DefaultConfig() *Config {
	return &Config{
		Enabled:              true,
		ShowDownloadComplete: true,
		ShowDownloadFailed:   true,
		ShowServiceStatus:    false, // Disabled by default to avoid spam
	}
}

// NewNotifier creates a new notifier with the given configuration.
func NewNotifier(cfg *Config, logger *logging.Logger) *Notifier {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	return &Notifier{
		logger:  logger,
		enabled: cfg.Enabled,
	}
}

// SetEnabled enables or disables notifications.
func (n *Notifier) SetEnabled(enabled bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.enabled = enabled
}

// IsEnabled returns whether notifications are enabled.
func (n *Notifier) IsEnabled() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.enabled
}

// DownloadComplete sends a notification for a successful download.
func (n *Notifier) DownloadComplete(jobName string, outputPath string) {
	if !n.IsEnabled() {
		return
	}

	title := "Download Complete"
	message := fmt.Sprintf("Job \"%s\" downloaded to:\n%s", truncate(jobName, 40), shortenPath(outputPath))

	if err := n.send(title, message); err != nil {
		n.logger.Warn().Err(err).Str("job", jobName).Msg("Failed to send download complete notification")
	}
}

// DownloadFailed sends a notification for a failed download.
func (n *Notifier) DownloadFailed(jobName string, errorMsg string) {
	if !n.IsEnabled() {
		return
	}

	title := "Download Failed"
	message := fmt.Sprintf("Job \"%s\" failed:\n%s", truncate(jobName, 40), truncate(errorMsg, 100))

	if err := n.send(title, message); err != nil {
		n.logger.Warn().Err(err).Str("job", jobName).Msg("Failed to send download failed notification")
	}
}

// ServiceStarted sends a notification when the service starts.
func (n *Notifier) ServiceStarted(userCount int) {
	if !n.IsEnabled() {
		return
	}

	title := "Rescale Interlink"
	message := fmt.Sprintf("Auto-download service started.\nMonitoring %d user(s).", userCount)

	if err := n.send(title, message); err != nil {
		n.logger.Warn().Err(err).Msg("Failed to send service started notification")
	}
}

// ServiceStopped sends a notification when the service stops.
func (n *Notifier) ServiceStopped() {
	if !n.IsEnabled() {
		return
	}

	title := "Rescale Interlink"
	message := "Auto-download service stopped."

	if err := n.send(title, message); err != nil {
		n.logger.Warn().Err(err).Msg("Failed to send service stopped notification")
	}
}

// NewJobsFound sends a notification when new jobs are found for download.
func (n *Notifier) NewJobsFound(count int) {
	if !n.IsEnabled() || count == 0 {
		return
	}

	title := "Rescale Interlink"
	message := fmt.Sprintf("Found %d new job(s) ready for download.", count)

	if err := n.send(title, message); err != nil {
		n.logger.Warn().Err(err).Msg("Failed to send new jobs notification")
	}
}

// send is the internal method that actually sends the notification.
func (n *Notifier) send(title, message string) error {
	// beeep.Notify is cross-platform:
	// - Windows: Uses toast notifications
	// - macOS: Uses NSUserNotificationCenter
	// - Linux: Uses D-Bus notifications
	return beeep.Notify(title, message, "")
}

// truncate shortens a string to maxLen, adding "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// shortenPath abbreviates a long path for display in notifications.
func shortenPath(path string) string {
	const maxLen = 60

	if len(path) <= maxLen {
		return path
	}

	// Try to show drive/root + ... + last 2 path components
	_, file := filepath.Split(path)
	parentDir := filepath.Base(filepath.Dir(path))

	// Build shortened path
	short := filepath.Join("...", parentDir, file)

	// Add volume/drive if there's room
	vol := filepath.VolumeName(path)
	if vol != "" && len(vol)+len(short)+1 <= maxLen {
		short = vol + string(filepath.Separator) + short
	}

	// If still too long, just truncate
	if len(short) > maxLen {
		return "..." + path[len(path)-(maxLen-3):]
	}

	return short
}

// Alert sends an alert notification (error level).
// This is for critical issues that require user attention.
func (n *Notifier) Alert(message string) {
	if !n.IsEnabled() {
		return
	}

	title := "Rescale Interlink Alert"

	// Use beeep.Alert which shows a more prominent notification on some platforms
	if err := beeep.Alert(title, message, ""); err != nil {
		// Fall back to regular notify
		if err := n.send(title, message); err != nil {
			n.logger.Error().Err(err).Str("message", message).Msg("Failed to send alert notification")
		}
	}
}

// Beep sends an audible beep notification.
// Useful for drawing attention without a visual notification.
func (n *Notifier) Beep() {
	if !n.IsEnabled() {
		return
	}

	// beeep.Beep() plays a system beep sound
	_ = beeep.Beep(beeep.DefaultFreq, beeep.DefaultDuration)
}

// ParseNotifyConfig parses notification settings from an INI section.
// Expected keys: enabled, show_download_complete, show_download_failed, show_service_status
func ParseNotifyConfig(settings map[string]string) *Config {
	cfg := DefaultConfig()

	if v, ok := settings["enabled"]; ok {
		cfg.Enabled = strings.ToLower(v) == "true"
	}
	if v, ok := settings["show_download_complete"]; ok {
		cfg.ShowDownloadComplete = strings.ToLower(v) == "true"
	}
	if v, ok := settings["show_download_failed"]; ok {
		cfg.ShowDownloadFailed = strings.ToLower(v) == "true"
	}
	if v, ok := settings["show_service_status"]; ok {
		cfg.ShowServiceStatus = strings.ToLower(v) == "true"
	}

	return cfg
}
