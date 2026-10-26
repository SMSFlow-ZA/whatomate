package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"regexp"
	"time"
)

// Set from the reviewed public commit when building the owner executable.
var SourceRevision string

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("extensions-worker-bootstrap", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	connectionPath := flags.String("connection", "", "root-private owner connection JSON")
	planPath := flags.String("plan", "", "strict reviewed bootstrap plan JSON")
	mode := flags.String("mode", "", "inspect, preview, apply or reconcile")
	receiptPath := flags.String("receipt", "", "new root-private receipt path")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *connectionPath == "" || *planPath == "" || *receiptPath == "" || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(SourceRevision) {
		return denied
	}
	if *mode != "inspect" && *mode != "preview" && *mode != "apply" && *mode != "reconcile" {
		return denied
	}
	raw, e := privateBytes(*planPath, 16*1024*1024)
	if e != nil {
		return denied
	}
	var p Plan
	if decode(raw, &p) != nil || p.Validate() != nil {
		return denied
	}
	p.rawSHA256 = digest(raw)
	raw, e = privateBytes(*connectionPath, 65536)
	if e != nil {
		return denied
	}
	var c Connection
	if decode(raw, &c) != nil {
		return denied
	}
	// Both create-only names are reserved and synced BEFORE connection or action.
	// A crash leaves the attempt and empty outcome as held evidence, never success.
	attempt, e := newReceipt(*receiptPath + ".attempt")
	if e != nil {
		return denied
	}
	a := struct{ Format, Mode, Nonce, PlanSHA256, SourceRevision string }{"worker-bootstrap-attempt-v1", *mode, p.Nonce, planHash(p), SourceRevision}
	if json.NewEncoder(attempt).Encode(a) != nil || attempt.Sync() != nil {
		attempt.Close()
		return denied
	}
	attempt.Close()
	receipt, e := newReceipt(*receiptPath)
	if e != nil {
		return denied
	}
	defer receipt.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.TimeoutSeconds+10)*time.Second)
	defer cancel()
	session, e := openOwner(ctx, c, p)
	var result Result
	if e != nil {
		result = (&Session{}).failedResult(p, e)
		result.Mode = *mode
	} else {
		defer session.DB.Close()
		result = session.Execute(ctx, p, *mode)
	}
	envelope := struct {
		SourceRevision string `json:"source_revision"`
		Result         Result `json:"result"`
	}{SourceRevision, result}
	if json.NewEncoder(receipt).Encode(envelope) != nil || receipt.Sync() != nil {
		return denied
	}
	// Only bounded typed diagnostics go to stdout/stderr. Observations and exact
	// plan provenance stay in the root-private durable receipt.
	if json.NewEncoder(stdout).Encode(struct{ Outcome, Code string }{result.Outcome, result.Code}) != nil {
		return denied
	}
	if result.Outcome == "unknown-held" || result.Outcome == "unresolved-held" || result.Outcome == "refused" {
		return denied
	}
	return nil
}
func main() {
	if run(os.Args[1:], os.Stdout, os.Stderr) != nil {
		_, _ = io.WriteString(os.Stderr, "worker_bootstrap_refused_or_held\n")
		os.Exit(1)
	}
}
