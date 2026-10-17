package reviewer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner records the service-manager commands and answers them.
type fakeRunner struct {
	calls  []string
	answer map[string]string // by first two words, e.g. "launchctl print"
	fail   map[string]bool
}

func (f *fakeRunner) run(name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	key := name
	if len(args) > 0 {
		key += " " + args[0]
	}
	if f.fail[key] {
		return "", os.ErrNotExist
	}
	return f.answer[key], nil
}

func TestInstallWritesALaunchAgentOnMacOS(t *testing.T) {
	home := t.TempDir()
	f := &fakeRunner{answer: map[string]string{"launchctl print": "state = running\n"}, fail: map[string]bool{}}
	o := InstallOptions{Home: home, GOOS: "darwin", UID: 501, Exec: "/usr/local/bin/plumbix", Args: []string{"--reviewer", "eduard@mac"}, LogPath: filepath.Join(home, ".plumbix", "review.log"), Path: "/opt/homebrew/bin:/usr/bin", Run: f.run}
	path, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, "Library", "LaunchAgents", "com.plumbix.review.plist") {
		t.Errorf("path = %s", path)
	}
	plist, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>com.plumbix.review</string>", "<string>/usr/local/bin/plumbix</string>", "<string>review</string>", "<string>--watch</string>", "<string>eduard@mac</string>",
		"<key>KeepAlive</key>", "<key>RunAtLoad</key>", filepath.Join(home, ".plumbix", "review.log"), "<string>/opt/homebrew/bin:/usr/bin</string>"} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("plist lacks %q:\n%s", want, plist)
		}
	}
	// An earlier agent is unloaded first (its absence is not an error), then the new one is loaded.
	if len(f.calls) != 2 || !strings.HasPrefix(f.calls[0], "launchctl bootout gui/501/com.plumbix.review") || f.calls[1] != "launchctl bootstrap gui/501 "+path {
		t.Errorf("calls = %v", f.calls)
	}
	st, err := Status(o)
	if err != nil || !st.Installed || !st.Running || st.Path != path {
		t.Errorf("status = %+v %v", st, err)
	}
	if err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the plist was left behind")
	}
	if last := f.calls[len(f.calls)-1]; !strings.HasPrefix(last, "launchctl bootout gui/501/com.plumbix.review") {
		t.Errorf("uninstall calls = %v", f.calls)
	}
	// Status without an agent: not installed, not running, no error.
	f.fail["launchctl print"] = true
	if st, err := Status(o); err != nil || st.Installed || st.Running {
		t.Errorf("status after uninstall = %+v %v", st, err)
	}
}

func TestInstallWritesASystemdUserUnitOnLinux(t *testing.T) {
	home := t.TempDir()
	f := &fakeRunner{answer: map[string]string{"systemctl --user": "active\n"}, fail: map[string]bool{}}
	o := InstallOptions{Home: home, GOOS: "linux", UID: 1000, Exec: "/usr/bin/plumbix", Args: []string{"--repo", "r_1"}, LogPath: filepath.Join(home, ".plumbix", "review.log"), Path: "/usr/bin", Run: f.run}
	path, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, ".config", "systemd", "user", "plumbix-review.service") {
		t.Errorf("path = %s", path)
	}
	unit, _ := os.ReadFile(path)
	for _, want := range []string{"ExecStart=/usr/bin/plumbix review --watch --repo r_1", "Restart=always", "Environment=PATH=/usr/bin", "WantedBy=default.target", "append:" + filepath.Join(home, ".plumbix", "review.log")} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if len(f.calls) != 2 || f.calls[0] != "systemctl --user daemon-reload" || f.calls[1] != "systemctl --user enable --now plumbix-review.service" {
		t.Errorf("calls = %v", f.calls)
	}
	if st, err := Status(o); err != nil || !st.Installed || !st.Running {
		t.Errorf("status = %+v %v", st, err)
	}
	if err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the unit was left behind")
	}
	if _, err := Install(InstallOptions{Home: home, GOOS: "windows", Run: f.run}); err == nil {
		t.Error("an unsupported platform was accepted")
	}
}
