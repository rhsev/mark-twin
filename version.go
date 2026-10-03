// Package twin is the engine behind the twin command: it reads sync-files
// through grubber, judges what is out of sync, and moves files with rsync.
// The command-line front end lives in cmd/twin; an interactive UI can sit on
// top of this package without touching it.
package twin

// Version is the one version string; --version and doctor print it.
const Version = "1.1.1"

// Build is the build stamp the Makefile injects (-X), so `--version` can
// tell one 1.0.0-dev build from the next.
var Build = "unstamped"
