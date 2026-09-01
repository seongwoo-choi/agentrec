//go:build linux

package cli

import "syscall"

// ioctlReadTermios is the request that reads terminal attributes; it succeeds
// only on a terminal.
const ioctlReadTermios = syscall.TCGETS
