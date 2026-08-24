// Package vtellog is a minimal, dependency-free debug logger shared by
// every vtel component (VPS server, desktop client, Android engine). Off by
// default; enabled via config ("debug": true, see vtelconfig.Config) or the
// VTEL_DEBUG=1 environment variable (checked at process start, so it also
// works for the Android engine subprocess with no Kotlin-side code change -
// VtelConfigStore writes "debug" straight into the same config.json the Go
// binary reads).
//
// Debug lines print through the same fmt.Printf-to-stdout path every other
// vtel log line already uses, so they show up wherever that does:
// journalctl on the VPS, the desktop app's Logs tab, and (via VtelEngine's
// stdout capture) the Android app's Logs tab / adb logcat.
package vtellog

import (
	"fmt"
	"os"
	"sync/atomic"
)

var enabled atomic.Bool

func init() {
	if os.Getenv("VTEL_DEBUG") == "1" {
		enabled.Store(true)
	}
}

// SetDebug turns debug logging on. It never turns it off - an operator's
// explicit VTEL_DEBUG=1 env var should win over a stale/false config field,
// and nothing else in vtel ever needs to disable debug logging mid-process.
func SetDebug(v bool) {
	if v {
		enabled.Store(true)
	}
}

// Enabled reports whether debug logging is currently on.
func Enabled() bool { return enabled.Load() }

// Debugf prints a debug line if debug logging is enabled; a no-op otherwise,
// so call sites can be sprinkled freely without a per-call Enabled() check.
func Debugf(format string, args ...any) {
	if enabled.Load() {
		fmt.Printf("[debug] "+format+"\n", args...)
	}
}
