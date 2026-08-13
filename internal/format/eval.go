package format

import (
	"errors"
	"fmt"

	"github.com/tejasa97/youtube_dlp/internal/value"
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

// evalContext carries the per-evaluation state. formats is the canonical
// worst-to-best view owned by Prepared; the evaluator never reverses it.
// availability caches per-object IsAvailable results so each canonical
// object is checked at most once per planning call.
type evalContext struct {
	formats         []*value.Object
	options         Options
	evaluation      EvaluationOptions
	incomplete      bool
	hasMergedFormat bool
	mergeCandidates int
	regexBudget     *regexEvalBudget
	availability    map[*value.Object]bool
}

// PlanSelect evaluates a selector into independent output plans.
func PlanSelect(info value.Info, selector Selector) ([]OutputPlan, error) {
	return PlanSelectWithOptions(info, selector, Options{})
}

// PlanSelectWithOptions canonicalizes formats then evaluates the selector AST.
func PlanSelectWithOptions(info value.Info, selector Selector, options Options) ([]OutputPlan, error) {
	return PlanSelectWithEvaluationOptions(info, selector, options, EvaluationOptions{})
}

// PlanSelectWithEvaluationOptions is the canonical planner entry point. It
// prepares the canonical view and evaluates the selector with the supplied
// evaluator-only options (currently: availability injection).
func PlanSelectWithEvaluationOptions(
	info value.Info,
	selector Selector,
	formatOptions Options,
	evaluationOptions EvaluationOptions,
) ([]OutputPlan, error) {
	prepared, err := Prepare(info, formatOptions)
	if err != nil {
		return nil, err
	}
	return prepared.PlanWithOptions(selector, evaluationOptions)
}

// Plan delegates to PlanWithOptions with the zero EvaluationOptions.
func (prepared Prepared) Plan(selector Selector) ([]OutputPlan, error) {
	return prepared.PlanWithOptions(selector, EvaluationOptions{})
}

// PlanWithOptions evaluates the selector against the canonical worst-to-best
// format view, applying the supplied EvaluationOptions (availability). It is
// the canonical planner entry point after Prepare; callers should prefer it
// over PlanSelectWithEvaluationOptions when they already hold a Prepared.
//
// PlanWithOptions is pure: no filesystem access, no subprocess execution,
// no FFmpeg probing, no network requests, no HTTP availability probes. The
// availability interface is invoked at most once per canonical object per
// call, and only for candidates that would otherwise be selected.
func (prepared Prepared) PlanWithOptions(selector Selector, evalOptions EvaluationOptions) ([]OutputPlan, error) {
	if len(prepared.formats) == 0 {
		return nil, ErrNoFormats
	}
	root, err := selector.rootNode()
	if err != nil {
		return nil, err
	}
	objects := make([]*value.Object, len(prepared.formats))
	byObject := make(map[*value.Object]normalizedFormat, len(prepared.formats))
	for index, item := range prepared.formats {
		// Canonical ordering is worst-to-best; preserve it explicitly. The
		// evaluator traverses from the correct end based on best/worst.
		objects[index] = item.Object
		byObject[item.Object] = item
	}
	ctx := evalContext{
		formats:         objects,
		options:         prepared.options,
		evaluation:      evalOptions,
		incomplete:      incompleteFormats(objects),
		hasMergedFormat: hasMergedFormat(objects),
		regexBudget:     newRegexEvalBudget(),
	}
	if evalOptions.Availability != nil {
		ctx.availability = make(map[*value.Object]bool, len(objects))
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
			selection, selectionErr := objectSelection(object)
			if selectionErr != nil {
				return nil, selectionErr
			}
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
		metadata := planMetadataFor(tracks, prepared)
		plans = append(plans, OutputPlan{Tracks: selections, Metadata: metadata})
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
		// yt-dlp filters only the formats list in the child context. The
		// incomplete/merged flags describe the original selection context and
		// intentionally remain unchanged after group-level filtering.
		// Availability cache is per-call; share it with the child so we still
		// observe the at-most-once-per-planning-call contract.
		if ctx.availability != nil {
			childCtx.availability = ctx.availability
		}
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

// isAvailableCached returns the cached availability for the supplied object,
// invoking the configured FormatAvailability callback on first reference. A
// nil availability accepts every candidate.
func (ctx *evalContext) isAvailableCached(object *value.Object) (bool, error) {
	if ctx.evaluation.Availability == nil {
		return true, nil
	}
	if cached, seen := ctx.availability[object]; seen {
		return cached, nil
	}
	ok, err := ctx.evaluation.Availability.IsAvailable(object)
	if err != nil {
		return false, err
	}
	ctx.availability[object] = ok
	return ok, nil
}

// mergeTrackGroups flattens the Cartesian product of left and right track
// groups, applying Python-compatible multistream suppression and the
// existing selector complexity limits. It explicitly drops the Go-only
// (format_id, URL) deduplication: selectors that yield the same object
// twice preserve both occurrences unless the stream suppression pass
// removes a later same-kind track.
func mergeTrackGroups(ctx *evalContext, left, right [][]*value.Object) ([][]*value.Object, error) {
	if len(left) == 0 || len(right) == 0 {
		return nil, nil
	}
	var merged [][]*value.Object
	for _, leftTracks := range left {
		for _, rightTracks := range right {
			combined := append(append([]*value.Object(nil), leftTracks...), rightTracks...)
			combined = applyMultistreamSuppression(combined, ctx.options)
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

// applyMultistreamSuppression walks the tracks in original order and removes
// storyboard tracks plus later same-kind tracks when the corresponding
// multistream option is false. It replaces the deleted
// dedupeMergeTracks helper; it never deduplicates by identity.
func applyMultistreamSuppression(tracks []*value.Object, options Options) []*value.Object {
	if len(tracks) <= 1 {
		return tracks
	}
	retained := make([]*value.Object, 0, len(tracks))
	seenVideo := false
	seenAudio := false
	for _, object := range tracks {
		if object == nil {
			continue
		}
		vcodec := readStringField(object, "vcodec")
		acodec := readStringField(object, "acodec")
		hasVideo := hasMediaKind(vcodec)
		hasAudio := hasMediaKind(acodec)
		if !hasVideo && !hasAudio {
			// Storyboard / nonmedia track: drop entirely from a merge.
			continue
		}
		if !options.AllowMultipleVideoStreams && hasVideo && seenVideo {
			continue
		}
		if !options.AllowMultipleAudioStreams && hasAudio && seenAudio {
			continue
		}
		if hasVideo {
			seenVideo = true
		}
		if hasAudio {
			seenAudio = true
		}
		retained = append(retained, object)
	}
	return retained
}

func evaluateAtom(ctx *evalContext, node *astNode) ([][]*value.Object, error) {
	spec := node.atom
	if spec.kind == atomFilterOnly {
		spec.kind = atomQuality
		spec.quality = Atom{OK: true, Best: true, Index: 1}
	}
	switch spec.kind {
	case atomAll:
		return atomAllMatches(ctx, node)
	case atomMergeAll:
		return atomMergeAllMatches(ctx, node)
	case atomDirectID:
		return atomDirectIDMatches(ctx, node, spec.text)
	case atomExtension:
		return atomExtensionMatches(ctx, node, spec.text)
	case atomQuality:
		return atomQualityMatches(ctx, node, spec.quality)
	default:
		return nil, selectorSyntax(node.span.start, node.span.end, "unknown atom")
	}
}

// atomAllMatches emits one OutputPlan per available matching format in
// best-to-worst order. The traversal is from the end of the canonical
// worst-to-best list toward the beginning.
func atomAllMatches(ctx *evalContext, node *astNode) ([][]*value.Object, error) {
	var outputs [][]*value.Object
	for index := len(ctx.formats) - 1; index >= 0; index-- {
		object := ctx.formats[index]
		matched, err := matchesFilters(object, node.filters, ctx.regexBudget)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		available, err := ctx.isAvailableCached(object)
		if err != nil {
			return nil, err
		}
		if !available {
			continue
		}
		outputs = append(outputs, []*value.Object{object})
		if len(outputs) > maxCommaOutputs {
			return nil, selectorLimit(node.span.start, node.span.end, "too many all outputs")
		}
	}
	return outputs, nil
}

// atomMergeAllMatches collects every playable matching format into one
// merged track list in best-to-worst order. Multistream suppression is
// applied before returning so the resulting track list matches what the
// yt-dlp pinned merge produces.
func atomMergeAllMatches(ctx *evalContext, node *astNode) ([][]*value.Object, error) {
	// Python checks availability in canonical worst-to-best order, then
	// constructs the merge in reverse so the best format is retained first.
	var checked []*value.Object
	for _, object := range ctx.formats {
		matched, err := matchesFilters(object, node.filters, ctx.regexBudget)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		if codecNotNone(object, "vcodec") || codecNotNone(object, "acodec") {
			available, err := ctx.isAvailableCached(object)
			if err != nil {
				return nil, err
			}
			if !available {
				continue
			}
			checked = append(checked, object)
		}
	}
	if len(checked) == 0 {
		return nil, nil
	}
	tracks := make([]*value.Object, len(checked))
	for index := range checked {
		tracks[len(checked)-1-index] = checked[index]
	}
	tracks = applyMultistreamSuppression(tracks, ctx.options)
	if len(tracks) == 0 {
		return nil, nil
	}
	if len(tracks) > maxMergeTerms {
		return nil, selectorLimit(node.span.start, node.span.end, "too many mergeall tracks")
	}
	return [][]*value.Object{tracks}, nil
}

// atomDirectIDMatches selects the canonical format whose normalized
// format_id exactly equals `id`, applying filters and availability. The
// canonical search is best-to-worst, matching yt-dlp's LazyList.reverse
// applied to the matches list.
func atomDirectIDMatches(ctx *evalContext, node *astNode, id string) ([][]*value.Object, error) {
	for index := len(ctx.formats) - 1; index >= 0; index-- {
		object := ctx.formats[index]
		actual, _ := object.Lookup("format_id").StringValue()
		if actual != id {
			continue
		}
		matched, err := matchesFilters(object, node.filters, ctx.regexBudget)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		available, err := ctx.isAvailableCached(object)
		if err != nil {
			return nil, err
		}
		if !available {
			continue
		}
		return [][]*value.Object{{object}}, nil
	}
	return nil, nil
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

// atomExtensionMatches selects the first playable matching extension format
// in canonical order. The canonical list is worst-to-best, so "first" means
// "the last element in the list" — best wins.
func atomExtensionMatches(ctx *evalContext, node *astNode, ext string) ([][]*value.Object, error) {
	video, audio, storyboard := extensionMediaKind(ext)
	for index := len(ctx.formats) - 1; index >= 0; index-- {
		object := ctx.formats[index]
		objectExt, _ := object.Lookup("ext").StringValue()
		if objectExt != ext {
			continue
		}
		matched, err := matchesFilters(object, node.filters, ctx.regexBudget)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		if !extensionObjectMatches(object, video, audio, storyboard) {
			continue
		}
		available, err := ctx.isAvailableCached(object)
		if err != nil {
			return nil, err
		}
		if !available {
			continue
		}
		return [][]*value.Object{{object}}, nil
	}
	if video && !ctx.hasMergedFormat {
		for index := len(ctx.formats) - 1; index >= 0; index-- {
			object := ctx.formats[index]
			objectExt, _ := object.Lookup("ext").StringValue()
			if objectExt != ext {
				continue
			}
			matched, err := matchesFilters(object, node.filters, ctx.regexBudget)
			if err != nil {
				return nil, err
			}
			if !matched {
				continue
			}
			if !codecNotNone(object, "vcodec") {
				continue
			}
			available, err := ctx.isAvailableCached(object)
			if err != nil {
				return nil, err
			}
			if !available {
				continue
			}
			return [][]*value.Object{{object}}, nil
		}
	}
	return nil, nil
}

// extensionObjectMatches mirrors the pinned selector atom filter for a known
// extension category. Storyboard requires both codecs == none. Audio requires
// acodec != none. Video requires both codecs != none (unless the separate
// fallback above kicks in).
func extensionObjectMatches(object *value.Object, video, audio, storyboard bool) bool {
	switch {
	case storyboard:
		return codecExplicitlyNone(object, "acodec") && codecExplicitlyNone(object, "vcodec")
	case audio:
		return codecNotNone(object, "acodec")
	case video:
		return codecNotNone(object, "acodec") && codecNotNone(object, "vcodec")
	}
	return false
}

// atomQualityMatches evaluates the best/worst selector. The canonical list is
// worst-to-best, so best iterates from the end and worst from the beginning.
// The `.N` index is one-based within available matching candidates.
//
// Ordering matches yt-dlp's pinned build_format_selector: no extra
// re-sorting happens — the canonical worst-to-best list is taken as the
// authoritative quality order, and LazyList.reverse makes the last
// canonical match the "best" pick.
func atomQualityMatches(ctx *evalContext, node *astNode, atom Atom) ([][]*value.Object, error) {
	if atom.indexTooLarge {
		return nil, nil
	}
	index := atom.Index
	if index < 1 {
		index = 1
	}
	find := func(fallback bool) (*value.Object, error) {
		seen := 0
		for step := 0; step < len(ctx.formats); step++ {
			position := step
			if atom.Best {
				position = len(ctx.formats) - 1 - step
			}
			object := ctx.formats[position]
			matched, err := matchesFilters(object, node.filters, ctx.regexBudget)
			if err != nil {
				return nil, err
			}
			if !matched {
				continue
			}
			if fallback {
				if !(codecNotNone(object, "vcodec") || codecNotNone(object, "acodec")) {
					continue
				}
			} else if !atomMatches(object, atom) {
				continue
			}
			ok, err := ctx.isAvailableCached(object)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			seen++
			if seen == index {
				return object, nil
			}
		}
		return nil, nil
	}
	selected, err := find(false)
	if err != nil {
		return nil, err
	}
	if selected == nil && atomAllowsIncompleteFallback(atom) && ctx.incomplete {
		selected, err = find(true)
		if err != nil {
			return nil, err
		}
	}
	if selected == nil {
		return nil, nil
	}
	return [][]*value.Object{{selected}}, nil
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

func atomMatches(candidate *value.Object, atom Atom) bool {
	switch {
	case atom.Media == AtomMediaCombined && !atom.Star:
		// Plain best/worst match combined audio+video formats under the
		// pinned combined-format rule.
		return codecNotNone(candidate, "vcodec") && codecNotNone(candidate, "acodec")
	case atom.Media == AtomMediaCombined && atom.Star:
		return codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec")
	case atom.Media == AtomMediaVideo && atom.Star:
		return codecNotNone(candidate, "vcodec") &&
			(codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec"))
	case atom.Media == AtomMediaAudio && atom.Star:
		return codecNotNone(candidate, "acodec") &&
			(codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec"))
	case atom.Media == AtomMediaVideo:
		return codecExplicitlyNone(candidate, "acodec") &&
			(codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec"))
	case atom.Media == AtomMediaAudio:
		return codecExplicitlyNone(candidate, "vcodec") &&
			(codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec"))
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
