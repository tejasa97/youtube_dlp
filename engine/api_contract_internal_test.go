package engine

import (
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/events"
)

func TestPublicEventKindsMatchOperationEvents(t *testing.T) {
	pairs := []struct {
		public   string
		internal events.Kind
	}{
		{EventExtracting, events.KindExtracting},
		{EventExtracted, events.KindExtracted},
		{EventDownloadStarting, events.KindStarting},
		{EventDownloadProgress, events.KindProgress},
		{EventDownloadRetry, events.KindRetry},
		{EventExtractorRetry, events.KindExtractorRetry},
		{EventDownloadCancelled, events.KindCancelled},
		{EventDownloadCompleted, events.KindCompleted},
		{EventFragmentStarting, events.KindFragmentStarting},
		{EventFragmentCompleted, events.KindFragmentCompleted},
		{EventPostprocessStarting, events.KindPostprocessStarting},
		{EventPostprocessProgress, events.KindPostprocessProgress},
		{EventPostprocessCompleted, events.KindPostprocessCompleted},
		{EventMetadataWarning, events.KindMetadataWarning},
		{EventMatchFilterSkipped, events.KindMatchFilterSkipped},
		{EventJavaScriptChallenge, events.KindJavaScriptChallenge},
	}
	for _, pair := range pairs {
		if pair.public != string(pair.internal) {
			t.Errorf("public event %q does not match internal event %q", pair.public, pair.internal)
		}
	}
}
