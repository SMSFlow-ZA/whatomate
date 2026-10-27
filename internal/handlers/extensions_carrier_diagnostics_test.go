package handlers

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shridarpatil/whatomate/internal/protectedcarrier"
	"github.com/zerodha/logf"
)

func TestProtectedCarrierDiagnosticsLogOnlyBoundedFailures(t *testing.T) {
	var output bytes.Buffer
	app := &App{Log: logf.New(logf.Opts{Writer: &output, Level: logf.WarnLevel})}
	app.observeProtectedCarrierDiagnostic(protectedcarrier.Diagnostic{Stage: protectedcarrier.DiagnosticAccepted, HTTPStatus: 200})
	app.observeProtectedCarrierDiagnostic(protectedcarrier.Diagnostic{Stage: protectedcarrier.DiagnosticStage("private-provider-text")})
	if output.Len() != 0 {
		t.Fatal("successful or unrecognized diagnostic stage must not emit log data")
	}
	for _, stage := range []protectedcarrier.DiagnosticStage{
		protectedcarrier.DiagnosticMessage, protectedcarrier.DiagnosticCredentials,
		protectedcarrier.DiagnosticRequest, protectedcarrier.DiagnosticTransport,
		protectedcarrier.DiagnosticResponse,
	} {
		output.Reset()
		app.observeProtectedCarrierDiagnostic(protectedcarrier.Diagnostic{Stage: stage, HTTPStatus: 401, ErrorCode: 190, ErrorSubcode: 463})
		log := output.String()
		for _, want := range []string{"Protected carrier submission outcome unconfirmed", string(stage), "http_status", "401", "provider_code", "190", "provider_subcode", "463"} {
			if !strings.Contains(log, want) {
				t.Fatalf("missing bounded diagnostic field %q", want)
			}
		}
		for _, forbidden := range []string{"recipient", "tenant", "account", "message_id", "private-provider-text"} {
			if strings.Contains(log, forbidden) {
				t.Fatal("protected identity or provider text entered log output")
			}
		}
	}
}
