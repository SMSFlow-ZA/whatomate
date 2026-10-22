//go:build !linux

package main

import "os"

func rootOwned(info os.FileInfo) bool { return false }
