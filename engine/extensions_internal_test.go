package engine_test

import (
	"github.com/tejasa97/youtube_dlp/engine"
	"github.com/tejasa97/youtube_dlp/internal/javascript/ejs"
	"github.com/tejasa97/youtube_dlp/internal/youtubepot"
)

var (
	_ engine.ChallengeSolver = (*ejs.Solver)(nil)
	_ engine.POTResolver     = (*youtubepot.Director)(nil)
)
