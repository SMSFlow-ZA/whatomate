package extensions

import "testing"

func TestProtectedAuthenticationKeepsFactorTruth(t *testing.T) {
	a := Actor{}
	if a.HasProtectedAuthentication() {
		t.Fatal("empty actor authenticated")
	}
	a.PrimarySessionAuthorized = true
	if !a.HasProtectedAuthentication() || a.MFA || a.MFAGeneration != 0 {
		t.Fatal("primary policy invented factor proof")
	}
	a.PrimarySessionAuthorized = false
	a.MFA = true
	if !a.HasProtectedAuthentication() {
		t.Fatal("factor authentication rejected")
	}
}
