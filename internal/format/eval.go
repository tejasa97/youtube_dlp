package format

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var (
	// ErrMultiOutput indicates the selector yields multiple independent outputs
	// that the legacy flat []Selection API cannot represent.
	ErrMultiOutput = errors.New("selector yields multiple independent outputs")
	// ErrSelectorLimit indicates a syntactically valid selector exceeded a
	// bounded evaluation limit. Product callers categorize this as invalid_input
	// while still using errors.Is(err, ErrSelectorLimit) for the sentinel.
	ErrSelectorLimit = errors.New("format selector exceeds limit")
)

type evalContext struct {
	formats         []*value.Object
	options         Options
	incomplete      bool
	hasMergedFormat bool
	mergeCandidates int
	regexBudget     *regexEvalBudget
}

// PlanSelect evaluates a selector into independent output plans.
func PlanSelect(info value.Info, selector Selector) ([]OutputPlan, error) {
	return PlanSelectWithOptions(info, selector, Options{})
}

// PlanSelectWithOptions canonicalizes formats then evaluates the selector AST.
func PlanSelectWithOptions(info value.Info, selector Selector, options Options) ([]OutputPlan, error) {
	prepared, err := Prepare(info, options)
	if err != nil {
		return nil, err
	}
	return prepared.Plan(selector)
}

// evaluationFormats returns a best-to-worst view over the canonical worst-
// to-best Prepared.formats list. It allocates a new slice, iterates the
// canonical list in reverse, and preserves the original object pointers so
// source/index lookup remains exact. The canonical Prepared.formats is never
// mutated. This is the narrow compatibility adapter required by PR 4; it
// preserves the legacy evaluator's best-first contract without changing any
// selector algorithms. PR 5 replaces this adapter when the evaluator
// consumes canonical worst-to-best directly.
func (prepared Prepared) evaluationFormats() []*value.Object {
	if len(prepared.formats) == 0 {
		return nil
	}
	out := make([]*value.Object, len(prepared.formats))
	for destination, source := range prepared.formats {
		out[len(prepared.formats)-1-destination] = source.Object
	}
	return out
}

// Plan evaluates selector against this canonical format view without preparing
// or mutating the formats a second time.
func (prepared Prepared) Plan(selector Selector) ([]OutputPlan, error) {
	if len(prepared.formats) == 0 {
		return nil, ErrNoFormats
	}
	root, err := selector.rootNode()
	if err != nil {
		return nil, err
	}
	objects := prepared.evaluationFormats()
	byObject := make(map[*value.Object]normalizedFormat, len(prepared.formats))
	for _, item := range prepared.formats {
		byObject[item.Object] = item
	}
	ctx := evalContext{
		formats:         objects,
		options:         prepared.options,
		incomplete:      incompleteFormats(objects),
		hasMergedFormat: hasMergedFormat(objects),
		regexBudget:     newRegexEvalBudget(),
	}
	trackGroups, err := evaluateNode(&ctx, root)
	if err != nil {
		return nil, err
	}
	if len(trackGroups) == 0 {
		return nil, ErrNoMatch
	}
	plans := make([]OutputPlan, 0, len(trackGroups))
	for _, tracks := range trackGroups {
		if len(tracks) > maxMergeTerms {
			return nil, selectorLimit(0, 0, "too many merge tracks in one output")
		}
		selections := make([]Selection, 0, len(tracks))
		for _, object := range tracks {
			selection := objectSelection(object)
			if item, ok := byObject[object]; ok {
				selection.setSourceFormatIndex(item.Source)
				selection.setNormalizedFormatIndex(item.Index)
			}
			headers, headerErr := mergeHeaders(prepared.info.Lookup("http_headers"), object.Lookup("http_headers"))
			if headerErr != nil {
				return nil, headerErr
			}
			selection.Headers = headers
			selections = append(selections, selection)
		}
		plans = append(plans, OutputPlan{Tracks: selections})
	}
	if len(plans) > maxCommaOutputs {
		return nil, selectorLimit(0, 0, "too many independent outputs")
	}
	return plans, nil
}

func evaluateNode(ctx *evalContext, node *astNode) ([][]*value.Object, error) {
	if node == nil {
		return nil, selectorSyntax(0, 0, "empty selector")
	}
	switch node.kind {
	case astComma:
		var combined [][]*value.Object
		for index := range node.children {
			branch, err := evaluateNode(ctx, &node.children[index])
			if err != nil {
				return nil, err
			}
			combined = append(combined, branch...)
			if err := enforceOutputCount(len(combined), node.span); err != nil {
				return nil, err
			}
		}
		return combined, nil
	case astPickFirst:
		if len(node.children) != 2 {
			return nil, selectorSyntax(node.span.start, node.span.end, "invalid pick-first node")
		}
		left, err := evaluateNode(ctx, &node.children[0])
		if err != nil {
			return nil, err
		}
		if len(left) > 0 {
			return left, nil
		}
		return evaluateNode(ctx, &node.children[1])
	case astMerge:
		if len(node.children) != 2 {
			return nil, selectorSyntax(node.span.start, node.span.end, "invalid merge node")
		}
		left, err := evaluateNode(ctx, &node.children[0])
		if err != nil {
			return nil, err
		}
		right, err := evaluateNode(ctx, &node.children[1])
		if err != nil {
			return nil, err
		}
		return mergeTrackGroups(ctx, left, right)
	case astGroup:
		if len(node.children) == 0 {
			return nil, nil
		}
		if len(node.children) != 1 {
			return nil, selectorSyntax(node.span.start, node.span.end, "invalid group node")
		}
		childCtx := *ctx
		filtered, err := filterFormats(ctx.formats, node.filters, ctx.regexBudget)
		if err != nil {
			return nil, err
		}
		childCtx.formats = filtered
		return evaluateNode(&childCtx, &node.children[0])
	case astAtom:
		return evaluateAtom(ctx, node)
	default:
		return nil, selectorSyntax(node.span.start, node.span.end, "unknown selector node")
	}
}

func filterFormats(formats []*value.Object, filters []Filter, budget *regexEvalBudget) ([]*value.Object, error) {
	if len(filters) == 0 {
		return formats, nil
	}
	filtered := make([]*value.Object, 0, len(formats))
	for _, object := range formats {
		matched, err := matchesFilters(object, filters, budget)
		if err != nil {
			return nil, err
		}
		if matched {
			filtered = append(filtered, object)
		}
	}
	return filtered, nil
}

func mergeTrackGroups(ctx *evalContext, left, right [][]*value.Object) ([][]*value.Object, error) {
	if len(left) == 0 || len(right) == 0 {
		return nil, nil
	}
	var merged [][]*value.Object
	for _, leftTracks := range left {
		for _, rightTracks := range right {
			combined := append(append([]*value.Object(nil), leftTracks...), rightTracks...)
			combined = dedupeMergeTracks(combined)
			if len(combined) == 0 {
				continue
			}
			merged = append(merged, combined)
			ctx.mergeCandidates++
			if ctx.mergeCandidates > maxCartesianCandidates {
				return nil, selectorLimit(0, 0, "merge cartesian product exceeds limit")
			}
			if err := enforceMergeTrackCount(len(combined), span{}); err != nil {
				return nil, err
			}
		}
	}
	return merged, nil
}

func dedupeMergeTracks(tracks []*value.Object) []*value.Object {
	if len(tracks) <= 1 {
		return tracks
	}
	seen := make(map[string]int, len(tracks))
	result := make([]*value.Object, 0, len(tracks))
	for _, object := range tracks {
		id, _ := object.Lookup("format_id").StringValue()
		url, _ := object.Lookup("url").StringValue()
		key := id + "\x00" + url
		if index, duplicate := seen[key]; duplicate {
			result[index] = object
			continue
		}
		seen[key] = len(result)
		result = append(result, object)
	}
	return result
}

func evaluateAtom(ctx *evalContext, node *astNode) ([][]*value.Object, error) {
	spec := node.atom
	if spec.kind == atomFilterOnly {
		spec.kind = atomQuality
		spec.quality = Atom{OK: true, Best: true, Index: 1}
	}
	switch spec.kind {
	case atomAll:
		return atomAllMatches(ctx.formats, node.filters, node.span, ctx.regexBudget)
	case atomMergeAll:
		var tracks []*value.Object
		// The evaluator adapter already presents the pinned canonical list in
		// best-to-worst order, so mergeall consumes it in forward order.
		for _, object := range ctx.formats {
			matched, err := matchesFilters(object, node.filters, ctx.regexBudget)
			if err != nil {
				return nil, err
			}
			if matched && (codecNotNone(object, "vcodec") || codecNotNone(object, "acodec")) {
				tracks = append(tracks, object)
				if len(tracks) > maxMergeTerms {
					return nil, selectorLimit(node.span.start, node.span.end, "too many mergeall tracks")
				}
			}
		}
		if len(tracks) == 0 {
			return nil, nil
		}
		return [][]*value.Object{tracks}, nil
	case atomDirectID:
		for _, object := range ctx.formats {
			id, _ := object.Lookup("format_id").StringValue()
			if id != spec.text {
				continue
			}
			matched, err := matchesFilters(object, node.filters, ctx.regexBudget)
			if err != nil {
				return nil, err
			}
			if matched {
				return [][]*value.Object{{object}}, nil
			}
		}
		return nil, nil
	case atomExtension:
		matches, err := extensionMatches(ctx, spec.text, node.filters)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, nil
		}
		return [][]*value.Object{{matches[0]}}, nil
	case atomQuality:
		matches, err := qualityMatches(ctx, spec.quality, node.filters)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, nil
		}
		return [][]*value.Object{{matches[0]}}, nil
	default:
		return nil, selectorSyntax(node.span.start, node.span.end, "unknown atom")
	}
}

func atomAllMatches(formats []*value.Object, filters []Filter, span span, budget *regexEvalBudget) ([][]*value.Object, error) {
	var outputs [][]*value.Object
	for _, candidate := range formats {
		matched, err := matchesFilters(candidate, filters, budget)
		if err != nil {
			return nil, err
		}
		if matched {
			outputs = append(outputs, []*value.Object{candidate})
			if len(outputs) > maxCommaOutputs {
				return nil, selectorLimit(span.start, span.end, "too many all outputs")
			}
		}
	}
	return outputs, nil
}

func enforceOutputCount(count int, span span) error {
	if count > maxCommaOutputs {
		return selectorLimit(span.start, span.end, "too many independent outputs")
	}
	return nil
}

func enforceMergeTrackCount(count int, span span) error {
	if count > maxMergeTerms {
		return selectorLimit(span.start, span.end, "too many merge tracks in one output")
	}
	return nil
}

func selectorLimit(start, end int, message string) error {
	if end < start {
		end = start
	}
	return fmt.Errorf("%w: %s", ErrSelectorLimit, (&SyntaxError{Start: start, End: end, Message: message}).Error())
}

func extensionMatches(ctx *evalContext, ext string, filters []Filter) ([]*value.Object, error) {
	video, audio, storyboard := extensionMediaKind(ext)
	var matches []*value.Object
	for _, object := range ctx.formats {
		objectExt, _ := object.Lookup("ext").StringValue()
		if objectExt != ext {
			continue
		}
		matched, err := matchesFilters(object, filters, ctx.regexBudget)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		switch {
		case storyboard:
			if codecExplicitlyNone(object, "acodec") && codecExplicitlyNone(object, "vcodec") {
				matches = append(matches, object)
			}
		case audio:
			if codecNotNone(object, "acodec") {
				matches = append(matches, object)
			}
		case video:
			if codecNotNone(object, "acodec") && codecNotNone(object, "vcodec") {
				matches = append(matches, object)
			}
		}
	}
	if len(matches) == 0 && video && !ctx.hasMergedFormat {
		for _, object := range ctx.formats {
			objectExt, _ := object.Lookup("ext").StringValue()
			if objectExt != ext {
				continue
			}
			matched, err := matchesFilters(object, filters, ctx.regexBudget)
			if err != nil {
				return nil, err
			}
			if !matched {
				continue
			}
			if codecNotNone(object, "vcodec") {
				matches = append(matches, object)
			}
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return orderAtomMatches(matches, Atom{OK: true, Best: true, Index: 1}, ctx.options), nil
}

func qualityMatches(ctx *evalContext, atom Atom, filters []Filter) ([]*value.Object, error) {
	if atom.indexTooLarge {
		return nil, nil
	}
	filtered := make([]*value.Object, 0, len(ctx.formats))
	for _, candidate := range ctx.formats {
		matched, err := matchesFilters(candidate, filters, ctx.regexBudget)
		if err != nil {
			return nil, err
		}
		if matched {
			filtered = append(filtered, candidate)
		}
	}
	matches := collectAtomMatches(filtered, atom)
	if len(matches) == 0 && atomAllowsIncompleteFallback(atom) && ctx.incomplete {
		matches = collectPlayable(filtered)
	}
	if len(matches) == 0 {
		return nil, nil
	}
	ordered := orderAtomMatches(matches, atom, ctx.options)
	index := atom.Index
	if index < 1 {
		index = 1
	}
	if index > len(ordered) {
		return nil, nil
	}
	if atom.Best {
		return []*value.Object{ordered[index-1]}, nil
	}
	return []*value.Object{ordered[len(ordered)-index]}, nil
}

func atomAllowsIncompleteFallback(atom Atom) bool {
	return atom.Media == AtomMediaCombined && !atom.Star
}

func incompleteFormats(formats []*value.Object) bool {
	if len(formats) == 0 {
		return false
	}
	allVideoNone := true
	allAudioNone := true
	for _, candidate := range formats {
		if !codecExplicitlyNone(candidate, "vcodec") {
			allVideoNone = false
		}
		if !codecExplicitlyNone(candidate, "acodec") {
			allAudioNone = false
		}
		if !allVideoNone && !allAudioNone {
			return false
		}
	}
	return allVideoNone || allAudioNone
}

func hasMergedFormat(formats []*value.Object) bool {
	for _, candidate := range formats {
		if codecNotNone(candidate, "vcodec") && codecNotNone(candidate, "acodec") {
			return true
		}
	}
	return false
}

func collectAtomMatches(formats []*value.Object, atom Atom) []*value.Object {
	matches := make([]*value.Object, 0, len(formats))
	for _, candidate := range formats {
		if atomMatches(candidate, atom) {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func collectPlayable(formats []*value.Object) []*value.Object {
	matches := make([]*value.Object, 0, len(formats))
	for _, candidate := range formats {
		if codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec") {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func atomMatches(candidate *value.Object, atom Atom) bool {
	switch {
	case atom.Media == AtomMediaCombined && !atom.Star:
		// Plain best/worst keep the port's historical quality-first selection across
		// playable formats so pinned compatibility fixtures remain stable.
		return codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec")
	case atom.Media == AtomMediaCombined && atom.Star:
		return codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec")
	case atom.Media == AtomMediaVideo && atom.Star:
		hasVideo, _ := candidateMediaKinds(candidate)
		return hasVideo && (codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec"))
	case atom.Media == AtomMediaAudio && atom.Star:
		_, hasAudio := candidateMediaKinds(candidate)
		return hasAudio && (codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec"))
	case atom.Media == AtomMediaVideo:
		return candidateMatchesKind(candidate, true, false, nil)
	case atom.Media == AtomMediaAudio:
		return candidateMatchesKind(candidate, false, true, nil)
	default:
		return false
	}
}

func codecNotNone(object *value.Object, key string) bool {
	text, ok := object.Lookup(key).StringValue()
	return !ok || text != "none"
}

func codecExplicitlyNone(object *value.Object, key string) bool {
	text, ok := object.Lookup(key).StringValue()
	return ok && text == "none"
}

func orderAtomMatches(matches []*value.Object, atom Atom, options Options) []*value.Object {
	ordered := append([]*value.Object(nil), matches...)
	if len(options.Sort) > 0 {
		return ordered
	}
	wantVideo := atom.Media == AtomMediaVideo
	wantAudio := atom.Media == AtomMediaAudio
	sort.SliceStable(ordered, func(left, right int) bool {
		l, r := ordered[left], ordered[right]
		if lp, rp := extractorPreference(l), extractorPreference(r); lp != rp {
			return lp > rp
		}
		ls, rs := formatScore(l, wantVideo, wantAudio), formatScore(r, wantVideo, wantAudio)
		if ls != rs {
			return ls > rs
		}
		if lr, rr := preferenceRank(l, options), preferenceRank(r, options); lr != rr {
			return lr > rr
		}
		return false
	})
	return ordered
}

func legacyChoiceToAST(choice Choice) (*astNode, error) {
	if len(choice.Terms) == 0 {
		return nil, selectorSyntax(0, 0, "empty choice")
	}
	var current *astNode
	for _, term := range choice.Terms {
		atom, err := legacyTermToAST(term)
		if err != nil {
			return nil, err
		}
		if current == nil {
			current = atom
			continue
		}
		current = &astNode{kind: astMerge, children: []astNode{*current, *atom}}
	}
	return current, nil
}

func legacyTermToAST(term Term) (*astNode, error) {
	spec := atomSpec{kind: atomDirectID, text: term.Name}
	if term.Name == "all" {
		spec.kind = atomAll
	} else if term.Name == "mergeall" {
		spec.kind = atomMergeAll
	} else if atom, ok := term.resolveAtom(); ok {
		spec = atomSpec{kind: atomQuality, quality: atom}
	} else if kind, ok := classifyExtensionToken(term.Name); ok {
		spec = atomSpec{kind: kind, text: term.Name}
	} else if atom, err := parseAtomSpec(term.Name, 0); err == nil {
		spec = atom
	}
	return &astNode{kind: astAtom, atom: spec, filters: append([]Filter(nil), term.Filters...)}, nil
}

func (term Term) resolveAtom() (Atom, bool) {
	if term.Atom.OK {
		atom := term.Atom
		if atom.Index < 1 {
			atom.Index = 1
		}
		return atom, true
	}
	return resolveLegacyAtomName(term.Name)
}

func legacyAlternativesToAST(alternatives []Choice) (*astNode, error) {
	if len(alternatives) == 0 {
		return nil, selectorSyntax(0, 0, "selector is empty")
	}
	var current *astNode
	for _, choice := range alternatives {
		branch, err := legacyChoiceToAST(choice)
		if err != nil {
			return nil, err
		}
		if current == nil {
			current = branch
			continue
		}
		current = &astNode{kind: astPickFirst, children: []astNode{*current, *branch}}
	}
	return current, nil
}
