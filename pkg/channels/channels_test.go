package channels_test

import (
	"context"
	"strings"
	"testing"

	"github.com/shridarpatil/whatomate/pkg/channels"
)

func TestPreviewCountsEncodingAndMultipartWithoutTrimming(t *testing.T) {
	cases := []struct {
		name, text, encoding string
		units, parts         int
	}{
		{"single GSM", strings.Repeat("A", 160), "GSM-7", 160, 1},
		{"multipart GSM", strings.Repeat("A", 161), "GSM-7", 161, 2},
		{"extension alphabet", strings.Repeat("^", 81), "GSM-7", 162, 2},
		{"single unicode", strings.Repeat("漢", 70), "UTF-16", 70, 1},
		{"multipart unicode", strings.Repeat("漢", 71), "UTF-16", 71, 2},
		{"surrogate pair", strings.Repeat("😀", 36), "UTF-16", 72, 2},
		{"line breaks", "Line 1\r\nLine 2", "GSM-7", 14, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := channels.PreviewSMS(tc.text, 3)
			if err != nil || got.Encoding != tc.encoding || got.Units != tc.units || got.Segments != tc.parts {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
	if _, err := channels.PreviewSMS(strings.Repeat("A", 460), 3); err == nil {
		t.Fatal("four segments must be rejected")
	}
	for _, text := range []string{"", " \n\t", string([]byte{0xff})} {
		if _, err := channels.PreviewSMS(text, 3); err == nil {
			t.Fatal("invalid SMS accepted")
		}
	}
	if _, err := channels.PreviewSMS("valid", 0); err == nil {
		t.Fatal("unbounded segment limit accepted")
	}
}

func TestRegistryRejectsDuplicateAndIncompatibleProviders(t *testing.T) {
	r := channels.NewRegistry()
	p := &fakeProvider{version: channels.APIVersion}
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(p); err == nil {
		t.Fatal("duplicate provider accepted")
	}
	if _, ok := r.Get("fake"); !ok {
		t.Fatal("registered provider missing")
	}
	if err := channels.NewRegistry().Register(&fakeProvider{version: 99}); err == nil {
		t.Fatal("incompatible provider accepted")
	}
}

func TestCredentialsHaveRedactedStringRepresentation(t *testing.T) {
	c := channels.Connection{ID: "connection-1", Credentials: channels.Credentials{"secret": "never-log-me"}}
	if strings.Contains(c.Credentials.String(), "never-log-me") {
		t.Fatal("credentials leak")
	}
}

type fakeProvider struct{ version int }

func (f *fakeProvider) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "fake", Name: "Fake", Version: "0.1.0", APIVersion: f.version, Channel: "sms"}
}
func (f *fakeProvider) Validate(context.Context, channels.Connection) (channels.ConnectionInfo, error) {
	return channels.ConnectionInfo{}, nil
}
func (f *fakeProvider) Send(context.Context, channels.Connection, channels.Message) (channels.Submission, error) {
	return channels.Submission{Status: channels.StatusSubmitted}, nil
}
func (f *fakeProvider) Reconcile(context.Context, channels.Connection, string) (channels.Submission, error) {
	return channels.Submission{}, nil
}
func (f *fakeProvider) Poll(context.Context, channels.Connection, string) (channels.EventPage, error) {
	return channels.EventPage{}, nil
}
