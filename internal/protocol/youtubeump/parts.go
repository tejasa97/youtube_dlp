package youtubeump

// UMP part ids from LuanRT/googlevideo protos/video_streaming/ump_part_id.proto
// at commit d2fa40d761034a286cf60ee033653307a1295b0c.
const (
	PartMediaHeader = 20
	PartMedia       = 21
	PartMediaEnd    = 22

	PartNextRequestPolicy            = 35
	PartFormatInitializationMetadata = 42
	PartSABRRedirect                 = 43
	PartSABRError                    = 44
	PartReloadPlayerResponse         = 46
	PartLiveMetadata                 = 31
	PartSABRContextUpdate            = 57
	PartStreamProtectionStatus       = 58
	PartSABRContextSendingPolicy     = 59
	PartEndOfTrack                   = 62
)

func isHandledPart(partType int) bool {
	switch partType {
	case PartMediaHeader, PartMedia, PartMediaEnd, PartFormatInitializationMetadata,
		PartNextRequestPolicy, PartEndOfTrack,
		PartSABRRedirect, PartSABRContextUpdate, PartSABRContextSendingPolicy:
		return true
	default:
		return isBenignSkippedPart(partType)
	}
}

// isBenignSkippedPart lists unknown UMP parts with no correctness effect on the
// bounded finite-VOD slice. The list is intentionally empty: every other part
// type is fail-closed.
func isBenignSkippedPart(partType int) bool {
	_ = partType
	return false
}

func isCriticalUnsupportedPart(partType int) bool {
	switch partType {
	case PartSABRError, PartReloadPlayerResponse, PartLiveMetadata, PartStreamProtectionStatus:
		return true
	default:
		return !isHandledPart(partType)
	}
}
