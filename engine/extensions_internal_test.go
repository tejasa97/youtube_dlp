package engine_test

import (
	"github.com/tejasa97/ytdlp-go/engine"
	"github.com/tejasa97/ytdlp-go/internal/javascript/ejs"
	"github.com/tejasa97/ytdlp-go/internal/youtubepot"
)

var (
	_ engine.ChallengeSolver = (*ejs.Solver)(nil)
	_ engine.POTResolver     = (*youtubepot.Director)(nil)
)
