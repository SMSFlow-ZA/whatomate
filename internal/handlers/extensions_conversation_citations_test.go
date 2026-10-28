package handlers

import (
	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"strings"
	"testing"
)

func citationFixture() ai.Result {
	s := knowledge.Source{DocumentID: "DEMO-SOP-001", Version: "2", ContentHash: strings.Repeat("a", 64), ManifestHash: strings.Repeat("b", 64), Page: knowledge.Page{Number: 3, Text: "Notify the designated reviewer before taking further action."}}
	s.Reference = s.ManifestHash + ":3"
	return ai.Result{Text: s.Page.Text, Citations: []knowledge.Source{s}}
}
func TestConversationCitationsDisplayReadableLabelsKeepInternalEvidence(t *testing.T) {
	r := citationFixture()
	before := r.Citations[0]
	got := extensionConversationAnswer(r)
	want := r.Text + "\n\nSources:\n- DEMO-SOP-001 | version 2 | page 3"
	if got.Handoff || got.Text != want {
		t.Fatalf("expected readable document/version/page citation, got %q", got.Text)
	}
	if strings.Contains(got.Text, before.ManifestHash) || strings.Contains(got.Text, before.ContentHash) {
		t.Fatal("internal digest appeared in handset citation")
	}
	if len(got.Citations) != 1 || got.Citations[0] != before.Reference || r.Citations[0] != before {
		t.Fatal("display change modified internal provenance")
	}
}

func TestConversationCitationsKeepHashLikeApprovedQuoteUnchanged(t *testing.T) {
	r := citationFixture()
	r.Text = "Approved example identifier: " + r.Citations[0].ManifestHash
	r.Citations[0].Page.Text = r.Text
	got := extensionConversationAnswer(r)
	if got.Handoff || !strings.HasPrefix(got.Text, r.Text+"\n\nSources:\n") || strings.Count(got.Text, r.Citations[0].ManifestHash) != 1 {
		t.Fatal("renderer must preserve literal source text and omit only the metadata suffix")
	}
}
func TestConversationCitationsIncludeReadableServerProvenanceInHandsetText(t *testing.T) {
	r := citationFixture()
	got := extensionConversationAnswer(r)
	if got.Handoff || got.Text == r.Text || !strings.HasPrefix(got.Text, r.Text+"\n\nSources:\n") || !strings.Contains(got.Text, "DEMO-SOP-001 | version 2 | page 3") || strings.Contains(got.Text, r.Citations[0].Reference) || len(got.Citations) != 1 {
		t.Fatal("handset reply omitted exact source provenance")
	}
	if got.Citations[0] != r.Citations[0].Reference {
		t.Fatal("structured provenance changed")
	}
}
func TestConversationCitationsDeduplicateAndPreserveSource(t *testing.T) {
	r := citationFixture()
	r.Citations = append(r.Citations, r.Citations[0])
	got := extensionConversationAnswer(r)
	if got.Handoff || strings.Count(got.Text, "- DEMO-SOP-001 | version 2 | page 3") != 1 || len(got.Citations) != 1 {
		t.Fatal("duplicate provenance not canonicalized")
	}
	if len(r.Citations) != 2 || r.Text != r.Citations[0].Page.Text {
		t.Fatal("renderer mutated result")
	}
}

func TestConversationCitationsReadableDisplayStillHasUTF8ByteBound(t *testing.T) {
	r := citationFixture()
	suffix := "\n\nSources:\n- DEMO-SOP-001 | version 2 | page 3"
	r.Text = strings.Repeat("x", extensionConversationReplyBytes-len(suffix)-len("界")) + "界"
	r.Citations[0].Page.Text = r.Text
	got := extensionConversationAnswer(r)
	if got.Handoff || len(got.Text) != extensionConversationReplyBytes || !strings.HasPrefix(got.Text, r.Text) {
		t.Fatal("exact byte-bound quote and readable label must fit without truncation")
	}
	r.Text += "x"
	r.Citations[0].Page.Text = r.Text
	if got := extensionConversationAnswer(r); !got.Handoff || len(got.Citations) != 0 {
		t.Fatal("over-bound quote and readable label must fail closed")
	}
}
func TestConversationCitationsFailClosed(t *testing.T) {
	for _, kind := range []string{"handoff", "missing", "unquoted", "wrong-reference", "bad-manifest", "bad-content", "bad-page", "metadata-newline", "metadata-bidi", "invalid-utf8", "too-long", "conflicting-reference"} {
		t.Run(kind, func(t *testing.T) {
			r := citationFixture()
			switch kind {
			case "handoff":
				r.Handoff = true
			case "missing":
				r.Citations = nil
			case "unquoted":
				r.Text = "Invented unsupported answer"
			case "wrong-reference":
				r.Citations[0].Reference = "forged"
			case "bad-manifest":
				r.Citations[0].ManifestHash = "not-a-sha"
			case "bad-content":
				r.Citations[0].ContentHash = "not-a-sha"
			case "bad-page":
				r.Citations[0].Page.Number = 0
			case "metadata-newline":
				r.Citations[0].DocumentID = "DOC\nFORGED HEADER"
			case "metadata-bidi":
				r.Citations[0].DocumentID = "DOC\u202eunsafe"
			case "invalid-utf8":
				r.Text = string([]byte{0xff})
			case "too-long":
				r.Text = strings.Repeat("x", 4096)
				r.Citations[0].Page.Text = r.Text
			case "conflicting-reference":
				s := r.Citations[0]
				s.DocumentID = "OTHER"
				r.Citations = append(r.Citations, s)
			}
			got := extensionConversationAnswer(r)
			if !got.Handoff || len(got.Citations) != 0 || got.Text != ai.FixedResponse {
				t.Fatal("unsafe handset citation accepted")
			}
		})
	}
}
func TestConversationCitationsNeverAppendUnquotedPageData(t *testing.T) {
	r := citationFixture()
	r.Citations[0].Page.Text = "Private unrelated marker.\n" + r.Text + "\nMore unrelated details."
	got := extensionConversationAnswer(r)
	if got.Handoff || strings.Contains(got.Text, "unrelated") {
		t.Fatal("renderer exposed extra page text")
	}
}
