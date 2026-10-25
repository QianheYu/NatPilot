package main

// Build metadata. Overridden at build time with -ldflags.

var (
	version   = "0.1.0"
	commit    = "dev"
	buildDate = "unknown"
	product   = "NatPilot"
)

func versionString() string {
	return product + " " + version + " (" + commit + ", " + buildDate + ")"
}
