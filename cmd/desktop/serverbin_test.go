package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeFS builds an exists() replacement from a set of paths, so the search can
// be exercised for any OS without touching the disk.
func fakeFS(paths ...string) func(string) bool {
	set := map[string]bool{}
	for _, p := range paths {
		set[p] = true
	}
	return func(p string) bool { return set[p] }
}

func TestServerCandidateNames(t *testing.T) {
	win := serverCandidateNames("windows", "amd64")
	// The release artifact name must be a candidate: this is the exact case that
	// used to fail with "executable file not found in %PATH%".
	if !contains(win, "natpilot-windows-amd64.exe") {
		t.Errorf("windows candidates missing the release artifact name: %v", win)
	}
	if !contains(win, "natpilot.exe") {
		t.Errorf("windows candidates missing natpilot.exe: %v", win)
	}
	// ".exe" must be preferred over the extensionless spelling.
	if indexOf(win, "natpilot.exe") > indexOf(win, "natpilot") {
		t.Errorf("natpilot.exe should be tried before the extensionless form: %v", win)
	}

	lin := serverCandidateNames("linux", "arm64")
	if !contains(lin, "natpilot-linux-arm64") {
		t.Errorf("linux candidates missing the release artifact name: %v", lin)
	}
	for _, n := range lin {
		if strings.HasSuffix(n, ".exe") {
			t.Errorf("linux candidates must not include .exe names: %v", lin)
		}
	}
}

// TestResolveServerBinWindowsReleaseLayout reproduces the reported failure: both
// release artifacts sit in the same folder, named after their target.
func TestResolveServerBinWindowsReleaseLayout(t *testing.T) {
	dir := `C:\Users\me\Downloads\natpilot`
	self := filepath.Join(dir, "natpilot-desktop-windows-amd64.exe")
	server := filepath.Join(dir, "natpilot-windows-amd64.exe")

	got := resolveServerBin("", self, "windows", "amd64", fakeFS(self, server))
	if got.Path != server {
		t.Errorf("Path = %q, want %q", got.Path, server)
	}
	if !got.Local {
		t.Errorf("Local = false, want true (the binary is on disk)")
	}
}

// TestResolveServerBinBundleLayout covers the per-platform bundle layout, where
// the files carry their plain names.
func TestResolveServerBinBundleLayout(t *testing.T) {
	dir := filepath.Join("dist", "windows-amd64")
	self := filepath.Join(dir, "natpilot-desktop.exe")
	server := filepath.Join(dir, "natpilot.exe")

	got := resolveServerBin("", self, "windows", "amd64", fakeFS(self, server))
	if got.Path != server {
		t.Errorf("Path = %q, want %q", got.Path, server)
	}
}

// TestResolveServerBinNeverPicksItself guards against the shell launching the
// shell when only the desktop binary is present.
func TestResolveServerBinNeverPicksItself(t *testing.T) {
	dir := `/opt/natpilot`
	self := filepath.Join(dir, "natpilot-desktop")
	got := resolveServerBin("", self, "linux", "amd64", fakeFS(self))
	if got.Local {
		t.Errorf("resolved to a local path that does not exist: %+v", got)
	}
	if got.Path != "natpilot" {
		t.Errorf("Path = %q, want the PATH fallback", got.Path)
	}
	for _, p := range got.Probed {
		if p == self {
			t.Errorf("the shell probed itself as a server candidate: %v", got.Probed)
		}
	}
}

// TestResolveServerBinSubdirectory covers the bin/ layout.
func TestResolveServerBinSubdirectory(t *testing.T) {
	base := filepath.Join("C:", "apps", "natpilot")
	self := filepath.Join(base, "natpilot-desktop.exe")
	server := filepath.Join(base, "bin", "natpilot.exe")

	got := resolveServerBin("", self, "windows", "amd64", fakeFS(server))
	if got.Path != server {
		t.Errorf("Path = %q, want %q", got.Path, server)
	}
}

// TestResolveServerBinExplicitWins checks that --server overrides the search.
func TestResolveServerBinExplicitWins(t *testing.T) {
	explicit := `D:\tools\natpilot.exe`
	got := resolveServerBin(explicit, `C:\x\natpilot-desktop.exe`, "windows", "amd64", fakeFS())
	if got.Path != explicit || !got.Local {
		t.Errorf("explicit path ignored: %+v", got)
	}
}

// TestResolveServerBinReportsProbedPaths checks the diagnostics that make the
// failure actionable.
func TestResolveServerBinReportsProbedPaths(t *testing.T) {
	// t.TempDir keeps the path valid for the host OS: a hard coded
	// "/srv/natpilot" becomes "\srv\natpilot" under filepath.Join on windows and
	// no longer matches the probed paths or the hint.
	dir := filepath.Join(t.TempDir(), "natpilot")
	self := filepath.Join(dir, "natpilot-desktop")
	got := resolveServerBin("", self, "linux", "amd64", fakeFS())

	if len(got.Probed) == 0 {
		t.Fatalf("no probed paths recorded")
	}
	// Both the sibling directory and its bin/ subdirectory must be covered.
	var sawSibling, sawBin bool
	for _, p := range got.Probed {
		if filepath.Dir(p) == dir {
			sawSibling = true
		}
		if filepath.Base(filepath.Dir(p)) == "bin" {
			sawBin = true
		}
	}
	if !sawSibling || !sawBin {
		t.Errorf("probed paths do not cover both directories: %v", got.Probed)
	}

	hint := got.hint("natpilot-desktop")
	// The hint renders native separators; compare in slash form so the
	// assertion holds on windows as well.
	slashHint := strings.ReplaceAll(hint, `\`, "/")
	slashDir := strings.ReplaceAll(dir, `\`, "/")
	for _, want := range []string{"--server", "natpilot-desktop", slashDir} {
		if !strings.Contains(slashHint, want) {
			t.Errorf("hint is missing %q:\n%s", want, hint)
		}
	}
}

// TestResolveServerBinNoExecutablePath covers a failed os.Executable().
func TestResolveServerBinNoExecutablePath(t *testing.T) {
	got := resolveServerBin("", "", runtime.GOOS, runtime.GOARCH, fakeFS())
	if got.Path != "natpilot" {
		t.Errorf("Path = %q, want the PATH fallback", got.Path)
	}
	if got.Local {
		t.Errorf("Local should be false when there is no executable path to anchor on")
	}
}

// TestSamePath guards the self-detection helper.
func TestSamePath(t *testing.T) {
	if !samePath("/a/b", "/a/b") {
		t.Errorf("identical paths should compare equal")
	}
	if samePath("/a/b", "/a/c") {
		t.Errorf("different paths compared equal")
	}
}

func contains(list []string, want string) bool { return indexOf(list, want) >= 0 }

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}
