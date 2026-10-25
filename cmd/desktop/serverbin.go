package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// This file locates the `natpilot` server binary that the desktop shell spawns.
//
// The resolution used to assume the binary was called exactly "natpilot[.exe]",
// which broke every user who downloaded a release artifact named after its
// target (natpilot-windows-amd64.exe). The names produced by the release build
// are therefore first-class candidates, and a failure now explains exactly which
// paths were probed.

// serverCandidateNames lists the file names to look for, most preferred first.
//
// It covers the layouts the project actually ships:
//
//	natpilot(.exe)                              a-per-platform bundle directory
//	natpilot-<os>-<arch>(.exe)                  the flat release artifacts
//	natpilot-server(.exe) / natpilot-server-…   an explicit server-only name
func serverCandidateNames(goos, goarch string) []string {
	base := []string{"natpilot"}
	if goos != "" {
		base = append(base, "natpilot-"+goos)
		if goarch != "" {
			base = append(base, "natpilot-"+goos+"-"+goarch)
		}
	}
	base = append(base, "natpilot-server")
	if goos != "" && goarch != "" {
		base = append(base, "natpilot-server-"+goos+"-"+goarch)
	}

	out := make([]string, 0, len(base)+1)
	for _, n := range base {
		if goos == "windows" {
			out = append(out, n+".exe")
		}
		out = append(out, n)
	}
	return out
}

// serverLookup is the outcome of resolving the server binary.
type serverLookup struct {
	// Path is what should be executed. It is never empty: when no local file is
	// found it falls back to the bare name, letting the OS resolve it via PATH.
	Path string
	// Local reports whether Path was found on disk next to the shell.
	Local bool
	// Probed lists every path that was examined, for diagnostics.
	Probed []string
}

// hint renders an actionable message for when the server could not be started.
func (l serverLookup) hint(shellName string) string {
	var b strings.Builder
	b.WriteString("could not find the `natpilot` server binary.\n\n")
	b.WriteString("  The desktop shell only draws the window; it launches the natpilot\n")
	b.WriteString("  server behind the scenes, and needs that binary to be present.\n\n")
	if len(l.Probed) > 0 {
		b.WriteString("  Looked for:\n")
		for _, p := range l.Probed {
			b.WriteString("    " + p + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("  Fix: put the server binary in the same folder as " + shellName + ",\n")
	b.WriteString("  or point at it explicitly:\n\n")
	b.WriteString("    " + shellName + " --server <path-to-natpilot>\n")
	b.WriteString("\n  Release archives are laid out as a per-platform folder containing\n")
	b.WriteString("  both `natpilot` and `natpilot-desktop`, in which case running the\n")
	b.WriteString("  shell with no arguments just works.")
	return b.String()
}

// resolveServerBin decides which binary to launch.
//
// Search order:
//  1. explicit (the --server flag)
//  2. a sibling of this executable, trying serverCandidateNames in order
//  3. a "bin" subdirectory next to this executable, same names
//  4. the bare name "natpilot", resolved through PATH by the OS
//
// exists is injected so the search is unit-testable without touching the disk.
// selfPath is skipped so the shell can never launch itself.
func resolveServerBin(explicit, selfPath, goos, goarch string, exists func(string) bool) serverLookup {
	if explicit != "" {
		return serverLookup{Path: explicit, Local: true, Probed: []string{explicit}}
	}

	var probed []string
	seen := map[string]bool{}

	try := func(dir string) (string, bool) {
		for _, name := range serverCandidateNames(goos, goarch) {
			cand := filepath.Join(dir, name)
			if seen[cand] {
				continue
			}
			seen[cand] = true
			probed = append(probed, cand)
			if selfPath != "" && samePath(cand, selfPath) {
				continue // never pick the shell itself
			}
			if exists(cand) {
				return cand, true
			}
		}
		return "", false
	}

	dirs := []string{}
	if selfPath != "" {
		base := filepath.Dir(selfPath)
		dirs = append(dirs, base, filepath.Join(base, "bin"))
	} else {
		dirs = append(dirs, ".", "bin")
	}
	for _, d := range dirs {
		if p, ok := try(d); ok {
			return serverLookup{Path: p, Local: true, Probed: probed}
		}
	}
	// Nothing local: fall back to PATH.
	sort.Strings(probed)
	return serverLookup{Path: "natpilot", Local: false, Probed: probed}
}

// samePath reports whether two paths refer to the same file, tolerating
// case differences on Windows and relative-vs-absolute spellings.
func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(aa, bb)
	}
	return aa == bb
}

// currentExecutable returns the path of the running binary, or "" if it cannot
// be determined.
func currentExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		return resolved
	}
	return exe
}

// fileExists is the production implementation of the injected exists function.
func fileExists(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !st.IsDir()
}
