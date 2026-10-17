package reviewer

import (
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Service names of the installed loop.
const (
	LaunchdLabel = "com.plumbix.review"
	SystemdUnit  = "plumbix-review.service"
)

// InstallOptions describe the loop to keep alive. Run executes the
// platform's service manager (launchctl or systemctl) and returns its
// output; tests inject a fake.
type InstallOptions struct {
	Home    string   // the user's home directory
	GOOS    string   // "darwin" or "linux"
	UID     int      // the user's id, for launchd's gui domain
	Exec    string   // the plumbix executable
	Args    []string // flags after "review --watch"
	LogPath string   // where the loop's output goes
	Path    string   // PATH for the loop, so claude is found
	Run     func(name string, args ...string) (string, error)
}

// ServiceStatus is what Status reports.
type ServiceStatus struct {
	Path      string
	Installed bool
	Running   bool
}

// ErrUnsupported means the platform has no supported service manager.
var ErrUnsupported = errors.New("plumbix review install supports macOS (launchd) and Linux (systemd user units)")

// Install writes the service definition and loads it, replacing an
// earlier one. It returns the file written.
func Install(o InstallOptions) (string, error) {
	path, err := servicePath(o)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(o.LogPath), 0o755); err != nil {
		return "", err
	}
	var content string
	switch o.GOOS {
	case "darwin":
		content = plist(o)
	case "linux":
		content = unit(o)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	switch o.GOOS {
	case "darwin":
		_, _ = o.Run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", o.UID, LaunchdLabel)) // absent is fine
		if out, err := o.Run("launchctl", "bootstrap", fmt.Sprintf("gui/%d", o.UID), path); err != nil {
			return path, fmt.Errorf("launchctl bootstrap: %w %s", err, strings.TrimSpace(out))
		}
	case "linux":
		if out, err := o.Run("systemctl", "--user", "daemon-reload"); err != nil {
			return path, fmt.Errorf("systemctl daemon-reload: %w %s", err, strings.TrimSpace(out))
		}
		if out, err := o.Run("systemctl", "--user", "enable", "--now", SystemdUnit); err != nil {
			return path, fmt.Errorf("systemctl enable: %w %s", err, strings.TrimSpace(out))
		}
	}
	return path, nil
}

// Uninstall stops the loop and removes its definition.
func Uninstall(o InstallOptions) error {
	path, err := servicePath(o)
	if err != nil {
		return err
	}
	switch o.GOOS {
	case "darwin":
		_, _ = o.Run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", o.UID, LaunchdLabel))
	case "linux":
		_, _ = o.Run("systemctl", "--user", "disable", "--now", SystemdUnit)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if o.GOOS == "linux" {
		_, _ = o.Run("systemctl", "--user", "daemon-reload")
	}
	return nil
}

// Status reports whether the loop is installed and running.
func Status(o InstallOptions) (ServiceStatus, error) {
	path, err := servicePath(o)
	if err != nil {
		return ServiceStatus{}, err
	}
	st := ServiceStatus{Path: path}
	if _, err := os.Stat(path); err == nil {
		st.Installed = true
	}
	switch o.GOOS {
	case "darwin":
		out, err := o.Run("launchctl", "print", fmt.Sprintf("gui/%d/%s", o.UID, LaunchdLabel))
		st.Running = err == nil && strings.Contains(out, "state = running")
	case "linux":
		out, err := o.Run("systemctl", "--user", "is-active", SystemdUnit)
		st.Running = err == nil && strings.TrimSpace(out) == "active"
	}
	return st, nil
}

func servicePath(o InstallOptions) (string, error) {
	switch o.GOOS {
	case "darwin":
		return filepath.Join(o.Home, "Library", "LaunchAgents", LaunchdLabel+".plist"), nil
	case "linux":
		return filepath.Join(o.Home, ".config", "systemd", "user", SystemdUnit), nil
	}
	return "", ErrUnsupported
}

func command(o InstallOptions) []string {
	return append([]string{o.Exec, "review", "--watch"}, o.Args...)
}

// plist is the launchd agent: start at login, restart when it exits, log
// to the review log, with the PATH the installer had so claude is found.
func plist(o InstallOptions) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + LaunchdLabel + `</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, a := range command(o) {
		fmt.Fprintf(&b, "    <string>%s</string>\n", html.EscapeString(a))
	}
	b.WriteString("  </array>\n")
	fmt.Fprintf(&b, "  <key>WorkingDirectory</key>\n  <string>%s</string>\n", html.EscapeString(o.Home))
	fmt.Fprintf(&b, "  <key>EnvironmentVariables</key>\n  <dict>\n    <key>PATH</key>\n    <string>%s</string>\n    <key>HOME</key>\n    <string>%s</string>\n  </dict>\n", html.EscapeString(o.Path), html.EscapeString(o.Home))
	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n  <key>KeepAlive</key>\n  <true/>\n  <key>ThrottleInterval</key>\n  <integer>10</integer>\n")
	fmt.Fprintf(&b, "  <key>StandardOutPath</key>\n  <string>%s</string>\n  <key>StandardErrorPath</key>\n  <string>%s</string>\n", html.EscapeString(o.LogPath), html.EscapeString(o.LogPath))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// unit is the systemd user service.
func unit(o InstallOptions) string {
	var parts []string
	for _, a := range command(o) {
		parts = append(parts, strconv.Quote(a))
	}
	for i, p := range parts { // systemd quoting: plain words need no quotes
		if !strings.ContainsAny(p[1:len(p)-1], " \t\"'\\$") {
			parts[i] = p[1 : len(p)-1]
		}
	}
	return "[Unit]\nDescription=Plumbix external reviewer (plumbix review --watch)\nAfter=network-online.target\n\n" +
		"[Service]\nExecStart=" + strings.Join(parts, " ") + "\nRestart=always\nRestartSec=5\n" +
		"Environment=PATH=" + o.Path + "\nWorkingDirectory=" + o.Home + "\n" +
		"StandardOutput=append:" + o.LogPath + "\nStandardError=append:" + o.LogPath + "\n\n" +
		"[Install]\nWantedBy=default.target\n"
}
