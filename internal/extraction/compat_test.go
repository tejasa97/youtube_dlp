package extraction_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tejasa97/youtube_dlp/engine"
	enginevalue "github.com/tejasa97/youtube_dlp/engine/value"
	"github.com/tejasa97/youtube_dlp/internal/extraction"
	internalvalue "github.com/tejasa97/youtube_dlp/internal/value"
)

func TestPublicContractAliasesPreserveIdentity(t *testing.T) {
	var publicRequest engine.Request
	var internalRequest extraction.Request
	if reflect.TypeOf(publicRequest) != reflect.TypeOf(internalRequest) {
		t.Fatal("request compatibility alias changed type identity")
	}

	var publicValue enginevalue.Value
	var internalValue internalvalue.Value
	if reflect.TypeOf(publicValue) != reflect.TypeOf(internalValue) {
		t.Fatal("value compatibility alias changed type identity")
	}

	if !errors.Is(extraction.ErrUnsupported, engine.ErrUnsupported) ||
		!errors.Is(engine.ErrUnsupported, extraction.ErrUnsupported) {
		t.Fatal("compatibility alias changed error identity")
	}
}
