package format

import (
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestObjectSelectionCarriesOnlyTypedExtractorFragmentProof(t *testing.T) {
	object := value.NewObject(
		value.Field{Key: "format_id", Value: value.String("hls")},
		value.Field{Key: "url", Value: value.String("https://cdn.example.test/vod.m3u8?token=secret")},
		value.Field{Key: "protocol", Value: value.String("m3u8_native")},
		value.Field{Key: "_fragment_resume_identity", Value: value.String("provider:rendition:7")},
		value.Field{Key: "_fragment_equivalence_kind", Value: value.String("content-identity")},
		value.Field{Key: "_fragment_equivalence_digest", Value: value.String(strings.Repeat("a", 64))},
		value.Field{Key: "_fragment_equivalence_scope_digest", Value: value.String(strings.Repeat("b", 64))},
		value.Field{Key: "_fragment_key_identity", Value: value.String("provider:key:epoch-1")},
	)
	selection, err := objectSelection(object)
	if err != nil {
		t.Fatal(err)
	}
	if selection.FragmentResumeIdentity != "provider:rendition:7" || selection.FragmentEquivalence.Kind != "content-identity" || selection.FragmentEquivalence.Value != strings.Repeat("a", 64) || selection.FragmentKeyIdentity != "provider:key:epoch-1" {
		t.Fatalf("selection=%#v", selection)
	}
}

func TestObjectSelectionRejectsUntypedOrStrongValidatorFragmentProof(t *testing.T) {
	for _, kind := range []string{"strong-validator", "content-identity"} {
		t.Run(kind, func(t *testing.T) {
			object := value.NewObject(
				value.Field{Key: "format_id", Value: value.String("hls")},
				value.Field{Key: "url", Value: value.String("https://cdn.example.test/vod.m3u8")},
				value.Field{Key: "_fragment_resume_identity", Value: value.String("provider:rendition:7")},
				value.Field{Key: "_fragment_equivalence_kind", Value: value.String(kind)},
				value.Field{Key: "_fragment_equivalence_digest", Value: value.String("Bearer.secret.payload")},
				value.Field{Key: "_fragment_equivalence_scope_digest", Value: value.String(strings.Repeat("b", 64))},
			)
			if _, err := objectSelection(object); err == nil {
				t.Fatal("accepted an unchecked or bearer-shaped proof")
			}
		})
	}
}
