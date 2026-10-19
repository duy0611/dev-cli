// Package idle stops a container nobody is connected to, from a watchdog that runs inside it.
package idle

import (
	_ "embed"
	"time"
)

// Script is the watchdog, a POSIX shell script that runs under busybox.
//
//go:embed watchdog.sh
var Script []byte

const (
	DefaultAfter = 30 * time.Minute
	DefaultTick  = time.Minute
)
