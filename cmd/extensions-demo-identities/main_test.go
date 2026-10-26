package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCLINoArgumentsAndEnvironmentRefusedWithoutDisclosure(t *testing.T) {
	var out bytes.Buffer
	if run(nil, &out) == nil || out.Len() != 0 {
		t.Fatal("missing arguments accepted")
	}
	t.Setenv("PGPASSWORD", "synthetic-private-sentinel")
	args := []string{"-config", "/root/not-created/config", "-plan", "/root/not-created/plan", "-ca-file", "/root/not-created/ca", "-attempt", "/root/not-created/attempt", "-password-out", "/root/not-created/passwords"}
	e := run(args, &out)
	if e == nil || out.Len() != 0 || strings.Contains(e.Error(), "sentinel") {
		t.Fatal("unsafe environment or output")
	}
}
