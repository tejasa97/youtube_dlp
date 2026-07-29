package format

// CompatibleExtensionForSelections computes the merged output container for
// retained tracks using the same rules as planner metadata. preferences may
// be nil to use yt-dlp's default MP4/WebM pass.
func CompatibleExtensionForSelections(tracks []Selection, preferences []string) string {
	vcodecs, acodecs, vexts, aexts := partitionSelectionCodecs(tracks)
	return compatibleExtension(vcodecs, acodecs, vexts, aexts, preferences)
}

func partitionSelectionCodecs(tracks []Selection) (vcodecs, acodecs, vexts, aexts []string) {
	for _, track := range tracks {
		if hasMediaKind(track.VCodec) {
			vcodecs = append(vcodecs, track.VCodec)
			vexts = append(vexts, track.Ext)
		}
		if hasMediaKind(track.ACodec) {
			acodecs = append(acodecs, track.ACodec)
			aexts = append(aexts, track.Ext)
		}
	}
	return vcodecs, acodecs, vexts, aexts
}
