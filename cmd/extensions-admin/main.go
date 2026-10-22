// extensions-admin is an offline, deployment-owned ER authorization tool.
// It neither creates primary identities nor changes passwords or MFA material.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
)

func readDocument(path string, out any) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil {
		return "", err
	}
	if err := decodeJSON(bytes.NewReader(b), out); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("extensions-admin", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "trusted host TOML configuration (required)")
	policyPath := flags.String("policy", "", "deployment-owned operator/profile policy JSON (required)")
	planPath := flags.String("plan", "", "exact replacement/revocation JSON; preview unless -apply")
	inspectUser := flags.String("inspect", "", "read current role, complete scopes and version for one allowed user UUID")
	apply := flags.Bool("apply", false, "commit reviewed replacement; omitted means read-only preview")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *policyPath == "" || (*planPath == "") == (*inspectUser == "") || *apply && *inspectUser != "" {
		return errors.New("require -config, -policy and exactly one of -plan or -inspect; -apply is valid only with a plan")
	}
	var policy Policy
	policyHash, err := readDocument(*policyPath, &policy)
	if err != nil {
		return err
	}
	if err := validatePolicy(policy); err != nil {
		return err
	}
	var plan Plan
	planHash := ""
	if *planPath != "" {
		planHash, err = readDocument(*planPath, &plan)
		if err != nil {
			return err
		}
		if _, err := validatePlan(policy, plan); err != nil {
			return err
		}
	} else if !exactID(*inspectUser) || !allowedTarget(policy, *inspectUser) {
		return errors.New("inspect target outside private policy")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return errors.New("host configuration unavailable")
	}
	db, err := database.NewPostgres(&cfg.Database, false)
	if err != nil {
		return errors.New("database unavailable")
	}
	raw, err := db.DB()
	if err != nil {
		return errors.New("database unavailable")
	}
	defer raw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Schema migration belongs to normal reviewed host startup. In particular,
	// previews and inspection must never install tables or grant credentials.
	var result Result
	if *inspectUser != "" {
		result, err = inspect(ctx, raw, policy, *inspectUser)
	} else {
		result, err = executePlan(ctx, raw, cfg, policy, plan, *apply, policyHash, planHash)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "extensions-admin:", err)
		os.Exit(1)
	}
}
