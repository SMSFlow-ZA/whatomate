//go:build windows

package main

func readPrivate(string) ([]byte, error)  { return nil, refused }
func reserveAttempt(string, Result) error { return refused }
