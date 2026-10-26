//go:build windows

package main

import "os"

func readPrivate(string) ([]byte, error)     { return nil, refused }
func validateOutput(string) error            { return refused }
func reserveOutput(string) (*os.File, error) { return nil, refused }
func finishOutput(string, *os.File) error    { return refused }
