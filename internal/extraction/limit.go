package extraction

import "context"

func LimitEntries(source EntrySequence, limit int) EntrySequence {
	return limitedEntries{source: source, limit: limit}
}

type limitedEntries struct {
	source EntrySequence
	limit  int
}

func (entries limitedEntries) Iterator() EntryIterator {
	return &limitedEntryIterator{source: entries.source.Iterator(), left: entries.limit}
}

type limitedEntryIterator struct {
	source EntryIterator
	left   int
}

func (iterator *limitedEntryIterator) Next(ctx context.Context) (Entry, bool, error) {
	if iterator.left == 0 {
		return Entry{}, false, nil
	}
	entry, ok, err := iterator.source.Next(ctx)
	if ok {
		iterator.left--
	}
	return entry, ok, err
}
