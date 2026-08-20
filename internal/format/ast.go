package format

import (
	"fmt"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

// astKind identifies selector AST node operators.
type astKind uint8

const (
	astComma astKind = iota
	astPickFirst
	astMerge
	astGroup
	astAtom
)

// atomKind classifies leaf selector atoms.
type atomKind uint8

const (
	atomDirectID atomKind = iota
	atomQuality
	atomExtension
	atomAll
	atomMergeAll
	atomFilterOnly // implicit best with leading filters
)

// astNode is a bounded format-selector syntax tree.
type astNode struct {
	kind     astKind
	children []astNode
	filters  []Filter
	atom     atomSpec
	span     span
}

type span struct {
	start int
	end   int
}

type atomSpec struct {
	kind atomKind
	// DirectID / Extension hold the raw token text.
	text string
	// Quality is set for best/worst atoms parsed by the atom lexer.
	quality Atom
}

// OutputPlan is one independent download product output. Tracks within a plan
// are merged when there is more than one.
//
// Metadata is the planner-owned clone of the output's merged-format
// dictionary. It is independent of Prepared.Info() and the
// extractor-owned input info: mutating it never reaches back into the
// extractor or the planner. For a single-track output Metadata is a
// defensive clone of the selected canonical format object; for a merged
// output it follows the yt-dlp merged-format dictionary rules
// (requested_formats, format, format_id, ext, protocol, language,
// format_note, filesize_approx, tbr, single-video and single-audio
// field promotion).
type OutputPlan struct {
	Tracks   []Selection
	Metadata value.Info
}

// PlanID returns a bounded label derived from the selected track IDs.
func (plan OutputPlan) PlanID() string {
	if len(plan.Tracks) == 0 {
		return "0"
	}
	if len(plan.Tracks) == 1 {
		return sanitizePlanID(plan.Tracks[0].ID)
	}
	parts := make([]string, len(plan.Tracks))
	for index, track := range plan.Tracks {
		parts[index] = sanitizePlanID(track.ID)
	}
	return sanitizePlanID(strings.Join(parts, "+"))
}

// DestinationSuffix returns a bounded, collision-resistant multi-output label.
// planIndex is the stable one-based output ordinal within the selector result.
func (plan OutputPlan) DestinationSuffix(planIndex int) string {
	if planIndex < 1 {
		planIndex = 1
	}
	return fmt.Sprintf("%d_%s", planIndex, plan.PlanID())
}

func sanitizePlanID(id string) string {
	if id == "" {
		return "0"
	}
	const max = 64
	if len(id) > max {
		id = id[:max]
	}
	out := make([]byte, 0, len(id))
	for index := 0; index < len(id); index++ {
		character := id[index]
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9', character == '_', character == '-', character == '.':
			out = append(out, character)
		case character == '+':
			out = append(out, '_')
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "0"
	}
	return string(out)
}
