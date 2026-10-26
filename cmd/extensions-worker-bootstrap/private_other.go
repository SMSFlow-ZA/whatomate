//go:build !linux

package main

import "os"

// This privileged command is Linux-only; native unit tests may still exercise
// pure contracts. Other platforms cannot read owner credentials or publish.
func privateBytes(string, int64) ([]byte, error) { return nil, denied }
func newReceipt(string) (*os.File, error)        { return nil, denied }
