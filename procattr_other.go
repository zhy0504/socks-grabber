//go:build !windows

package main

import "os/exec"

func applyPlatformProcAttr(cmd *exec.Cmd) {}
