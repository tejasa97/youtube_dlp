package youtubeump

import (
	"bytes"
	"fmt"
	"sort"
)

// sabrContextEntry is one stored SABR context value.
type sabrContextEntry struct {
	Type  int32
	Scope int32
	Value []byte
}

// sabrContextState is the per-downloader finite SABR context machine.
//
// Policy ordering matches LuanRT/googlevideo SabrStream.handleSabrContextSendingPolicy
// at commit d2fa40d761034a286cf60ee033653307a1295b0c: start, then stop, then discard.
// Contradictory start+stop of the same type in one policy is therefore resolved by that
// order (net inactive). Discard removes the stored value only; an orphaned active mark
// is inert because marshalling iterates stored entries exclusively.
type sabrContextState struct {
	entries map[int32]sabrContextEntry
	active  map[int32]struct{}
	total   int
}

func newSabrContextState() *sabrContextState {
	return &sabrContextState{
		entries: make(map[int32]sabrContextEntry),
		active:  make(map[int32]struct{}),
	}
}

func (state *sabrContextState) clone() *sabrContextState {
	if state == nil {
		return newSabrContextState()
	}
	out := &sabrContextState{
		entries: make(map[int32]sabrContextEntry, len(state.entries)),
		active:  make(map[int32]struct{}, len(state.active)),
		total:   state.total,
	}
	for key, entry := range state.entries {
		out.entries[key] = sabrContextEntry{
			Type:  entry.Type,
			Scope: entry.Scope,
			Value: bytes.Clone(entry.Value),
		}
	}
	for key := range state.active {
		out.active[key] = struct{}{}
	}
	return out
}

func (state *sabrContextState) applyUpdate(update sabrContextUpdateDirective) error {
	if update.Type <= 0 {
		return fmt.Errorf("%w: context type must be positive", ErrInvalidContextState)
	}
	if len(update.Value) == 0 || len(update.Value) > MaxSabrContextValueBytes {
		return fmt.Errorf("%w: invalid context value size", ErrInvalidContextState)
	}
	if existing, ok := state.entries[update.Type]; ok {
		if update.WritePolicy == SabrContextWriteKeepExisting {
			return nil
		}
		if err := state.replaceEntry(existing, update); err != nil {
			return err
		}
	} else {
		if err := state.insertEntry(update); err != nil {
			return err
		}
	}
	if update.SendByDefault {
		state.active[update.Type] = struct{}{}
	}
	return nil
}

func (state *sabrContextState) insertEntry(update sabrContextUpdateDirective) error {
	if len(state.entries) >= MaxSabrContexts {
		return fmt.Errorf("%w: context count bound exceeded", ErrInvalidContextState)
	}
	if err := state.addTotal(len(update.Value)); err != nil {
		return err
	}
	state.entries[update.Type] = sabrContextEntry{
		Type:  update.Type,
		Scope: update.Scope,
		Value: bytes.Clone(update.Value),
	}
	return nil
}

func (state *sabrContextState) replaceEntry(existing sabrContextEntry, update sabrContextUpdateDirective) error {
	delta := len(update.Value) - len(existing.Value)
	if delta > 0 {
		if err := state.addTotal(delta); err != nil {
			return err
		}
	} else {
		state.total += delta
	}
	state.entries[update.Type] = sabrContextEntry{
		Type:  update.Type,
		Scope: update.Scope,
		Value: bytes.Clone(update.Value),
	}
	return nil
}

func (state *sabrContextState) applySendingPolicy(policy sabrContextSendingPolicyDirective) error {
	// Deterministic pinned order: start -> stop -> discard.
	for _, typ := range policy.Start {
		if typ <= 0 {
			return fmt.Errorf("%w: policy type must be positive", ErrInvalidContextState)
		}
		state.active[typ] = struct{}{}
	}
	for _, typ := range policy.Stop {
		if typ <= 0 {
			return fmt.Errorf("%w: policy type must be positive", ErrInvalidContextState)
		}
		delete(state.active, typ)
	}
	for _, typ := range policy.Discard {
		if typ <= 0 {
			return fmt.Errorf("%w: policy type must be positive", ErrInvalidContextState)
		}
		if existing, ok := state.entries[typ]; ok {
			state.total -= len(existing.Value)
			delete(state.entries, typ)
		}
	}
	return nil
}

func (state *sabrContextState) addTotal(n int) error {
	if n < 0 {
		return ErrInvalidContextState
	}
	if n > MaxSabrContextValueBytesTotal-state.total {
		return fmt.Errorf("%w: cumulative context value bound exceeded", ErrInvalidContextState)
	}
	state.total += n
	return nil
}

func (state *sabrContextState) appendToStreamer(buf []byte) []byte {
	if state == nil || len(state.entries) == 0 {
		return buf
	}
	activeTypes := make([]int32, 0, len(state.entries))
	unsentTypes := make([]int32, 0, len(state.entries))
	for typ := range state.entries {
		if _, ok := state.active[typ]; ok {
			activeTypes = append(activeTypes, typ)
		} else {
			unsentTypes = append(unsentTypes, typ)
		}
	}
	sort.Slice(activeTypes, func(i, j int) bool { return activeTypes[i] < activeTypes[j] })
	sort.Slice(unsentTypes, func(i, j int) bool { return unsentTypes[i] < unsentTypes[j] })
	for _, typ := range activeTypes {
		entry := state.entries[typ]
		var nested []byte
		nested = appendProtobufVarint(nested, fStreamerSabrContextType, uint64(entry.Type))
		nested = appendProtobufBytes(nested, fStreamerSabrContextValue, entry.Value)
		buf = appendProtobufBytes(buf, fStreamerCtxSabrContexts, nested)
	}
	if len(unsentTypes) > 0 {
		buf = appendProtobufPackedInt32(buf, fStreamerCtxUnsentSabrContexts, unsentTypes)
	}
	return buf
}

func appendProtobufPackedInt32(buf []byte, field uint64, values []int32) []byte {
	var packed []byte
	for _, value := range values {
		packed = appendU64(packed, uint64(uint32(value)))
	}
	return appendProtobufBytes(buf, field, packed)
}

// redirectTracker records committed SABR endpoints for loop/budget detection.
type redirectTracker struct {
	seen  map[string]struct{}
	count int
}

func newRedirectTracker(initialURL string) *redirectTracker {
	tracker := &redirectTracker{seen: make(map[string]struct{}, 8)}
	if initialURL != "" {
		tracker.seen[initialURL] = struct{}{}
	}
	return tracker
}

func (tracker *redirectTracker) validate(url string) error {
	if tracker == nil {
		return nil
	}
	if _, ok := tracker.seen[url]; ok {
		return ErrRedirectLoop
	}
	if tracker.count >= MaxDirectiveRedirects {
		return ErrRedirectBudget
	}
	return nil
}

func (tracker *redirectTracker) record(url string) {
	if tracker == nil || url == "" {
		return
	}
	tracker.seen[url] = struct{}{}
	tracker.count++
}
