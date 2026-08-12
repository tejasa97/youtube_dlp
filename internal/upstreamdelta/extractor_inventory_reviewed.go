package upstreamdelta

import (
	"fmt"
	"strings"
)

// reviewedInventory preserves fixture-backed or otherwise reviewed rationale
// text for rows whose classification is already determined by exactAliases or
// normalized Go key matches. Overrides are keyed only by upstream IE class
// names that exist in the pinned registry; they cannot invent rows or promote
// unsupported classes. Reviewed unsupported rows may carry an explicit status
// without pretending that an exact Go mapping exists.
type reviewedInventoryEntry struct {
	rationale     string
	status        string
	allowUnmapped bool
}

var reviewedInventory = map[string]reviewedInventoryEntry{
	"TwitchClipsIE": {
		rationale: "fixture-backed exact Twitch class adapter over the shared backend; anonymous public clips only",
	},
	"TwitchCollectionIE": {
		rationale: "fixture-backed exact Twitch class adapter over the shared backend; anonymous public collections only",
	},
	"TwitchStreamIE": {
		rationale: "fixture-backed exact Twitch class adapter over the shared backend; anonymous public live/rerun only",
	},
	"TwitchVideosClipsIE": {
		rationale: "fixture-backed exact Twitch class adapter over the shared backend; bounded anonymous public clips playlist only",
	},
	"TwitchVideosCollectionsIE": {
		rationale: "fixture-backed exact Twitch class adapter over the shared backend; bounded anonymous public collections playlist only",
	},
	"TwitchVideosIE": {
		rationale: "fixture-backed exact Twitch class adapter over the shared backend; bounded anonymous public videos/profile playlist only",
	},
	"TwitchVodIE": {
		rationale: "fixture-backed exact Twitch class adapter over the shared backend; anonymous public VODs only",
	},
	"AmHistoryChannelIE": {
		rationale: "fixture-backed configuration-driven Discovery adapter with strict routing and product registry evidence",
	},
	"AnimalPlanetIE": {
		rationale: "fixture-backed configuration-driven Discovery adapter with strict routing and product registry evidence",
	},
	"CookingChannelIE": {
		rationale: "fixture-backed configuration-driven Discovery adapter with strict routing and product registry evidence",
	},
	"DPlayIE": {
		rationale: "fixture-backed legacy playback adapter with strict routing and product registry evidence",
	},
	"DestinationAmericaIE": {
		rationale: "fixture-backed configuration-driven Discovery adapter with strict routing and product registry evidence",
	},
	"DiscoveryLifeIE": {
		rationale: "fixture-backed configuration-driven Discovery adapter with strict routing and product registry evidence",
	},
	"DiscoveryNetworksDeIE": {
		rationale: "fixture-backed Loma CMS and Hyoga playback adapter with fallback and product registry evidence",
	},
	"DiscoveryPlusIE": {
		rationale: "fixture-backed Discovery Plus playback adapter with strict routing and product registry evidence",
	},
	"DiscoveryPlusIndiaIE": {
		rationale: "fixture-backed India playback adapter with strict routing and product registry evidence",
	},
	"DiscoveryPlusIndiaShowIE": {
		rationale: "fixture-backed bounded multi-season show pagination and product registry evidence",
	},
	"DiscoveryPlusItalyIE": {
		rationale: "fixture-backed Italy playback adapter with strict routing and product registry evidence",
	},
	"DiscoveryPlusItalyShowIE": {
		rationale: "fixture-backed bounded multi-season show pagination and product registry evidence",
	},
	"FoodNetworkIE": {
		rationale: "fixture-backed configuration-driven Discovery adapter with strict routing and product registry evidence",
	},
	"GoDiscoveryIE": {
		rationale: "fixture-backed configuration-driven Discovery adapter with strict routing and product registry evidence",
	},
	"HGTVDeIE": {
		rationale: "fixture-backed Hyoga playback adapter restricted to the public sendungen route",
	},
	"HGTVUsaIE": {
		rationale: "fixture-backed configuration-driven Discovery adapter with strict routing and product registry evidence",
	},
	"InvestigationDiscoveryIE": {
		rationale: "fixture-backed configuration-driven Discovery adapter with strict routing and product registry evidence",
	},
	"ScienceChannelIE": {
		rationale: "fixture-backed configuration-driven Discovery adapter with strict routing and product registry evidence",
	},
	"TLCIE": {
		rationale: "fixture-backed configuration-driven Discovery adapter with strict routing and product registry evidence",
	},
	"TravelChannelIE": {
		rationale: "fixture-backed configuration-driven Discovery adapter with strict routing and product registry evidence",
	},
	"NhkForSchoolBangumiIE": {
		rationale: "fixture-backed Go extractor with version-ID replacement and chapter evidence",
	},
	"NhkForSchoolProgramListIE": {
		rationale: "fixture-backed Go extractor with bangumi re-entry evidence",
	},
	"NhkForSchoolSubjectIE": {
		rationale: "fixture-backed Go extractor with allowlisted subject playlist evidence",
	},
	"NhkRadioNewsPageIE": {
		rationale: "fixture-backed Go extractor with transparent Radiru handoff evidence",
	},
	"NhkRadiruIE": {
		rationale: "fixture-backed Go extractor for Radiru on-demand episode/playlist evidence",
	},
	"NhkRadiruLiveIE": {
		rationale: "fixture-backed Go extractor with area option and live HLS evidence",
	},
	"NhkVodIE": {
		rationale: "fixture-backed Go extractor for NHK World VOD video/audio/clip evidence",
	},
	"NhkVodProgramIE": {
		rationale: "fixture-backed Go extractor for NHK World program playlist re-entry evidence",
	},
	"MicrosoftBuildIE": {
		rationale: "fixture-backed strict Build sessions routes with bounded Medius transparent re-entry and product download evidence",
	},
	"MicrosoftEmbedIE": {
		rationale: "fixture-backed strict Microsoft videoplayer route with native manifest and direct-media product evidence",
	},
	"MicrosoftLearnEpisodeIE": {
		rationale: "fixture-backed strict Learn show child route with bounded public video API and native media formats",
	},
	"MicrosoftLearnPlaylistIE": {
		rationale: "fixture-backed strict Learn shows/events routes with lazy reusable bounded API pagination",
	},
	"MicrosoftLearnSessionIE": {
		rationale: "fixture-backed strict Learn event child route with validated scoped Medius transparent re-entry",
	},
	"MicrosoftMediusIE": {
		rationale: "fixture-backed exact Medius Embed routes with canonical discovery and native ISM product evidence",
	},
	"NiconicoHistoryIE": {
		status:        ExtractorAuthOrAntiBot,
		allowUnmapped: true,
		rationale:     "unsupported: pinned history/likes API requires authenticated cookies; no registered native mapping or product evidence is claimed",
	},
	"NiconicoIE": {
		status:    ExtractorPartiallySupported,
		rationale: "fixture-backed anonymous v3_guest watch/shorts and access-rights HLS; authentication, entitlements, sensitive, geo, comments, and future service behavior remain outside the claim",
	},
	"NiconicoLiveIE": {
		status:        ExtractorAuthOrAntiBot,
		allowUnmapped: true,
		rationale:     "unsupported: pinned live extraction requires websocket seat, stream-cookie, heartbeat, and live lifecycle behavior; no registered native mapping or product evidence is claimed",
	},
	"NiconicoPlaylistIE": {
		status:    ExtractorPartiallySupported,
		rationale: "fixture-backed anonymous mylist API pagination with reusable bounded child routing; authenticated/private and unproven service states remain outside the claim",
	},
	"NiconicoSeriesIE": {
		status:    ExtractorPartiallySupported,
		rationale: "fixture-backed anonymous series API pagination with reusable bounded child routing; authenticated/private and unproven service states remain outside the claim",
	},
	"NiconicoUserIE": {
		status:    ExtractorPartiallySupported,
		rationale: "fixture-backed anonymous user video API pagination with reusable bounded child routing; authenticated/private and unproven service states remain outside the claim",
	},
	"NicovideoSearchDateIE": {
		status:        ExtractorNewBackend,
		allowUnmapped: true,
		rationale:     "unsupported: pinned date search uses wall-clock recursive interval splitting and has no registered exact class mapping, fixed bounded native contract, or product evidence",
	},
	"NicovideoSearchIE": {
		status:    ExtractorPartiallySupported,
		rationale: "fixture-backed anonymous bounded pseudo-search HTML pagination; date search, live service drift, and unproven response families remain outside the claim",
	},
	"NicovideoSearchURLIE": {
		status:    ExtractorPartiallySupported,
		rationale: "fixture-backed anonymous bounded search URL HTML pagination with strict query policy and child routing",
	},
	"NicovideoTagURLIE": {
		status:    ExtractorPartiallySupported,
		rationale: "fixture-backed anonymous bounded tag HTML pagination with strict query policy and child routing",
	},
	"TedEmbedIE": {
		rationale: "fixture-backed TED embed-to-canonical-talk transparent routing with strict route overlap",
	},
	"TedPlaylistIE": {
		rationale: "fixture-backed TED public playlist metadata and bounded child identity extraction",
	},
	"TedSeriesIE": {
		rationale: "fixture-backed TED public series season filtering and child identity extraction",
	},
	"TedTalkIE": {
		rationale: "fixture-backed TED public Next metadata, isolated direct/HLS/audio playback, sidecars, and chapters",
	},
	"PRXSeriesSearchIE": {
		rationale: "pinned prxseries opaque search key is registered with bounded fixture-backed CMS paging",
	},
	"PRXStoriesSearchIE": {
		rationale: "pinned prxstories opaque search key is registered with bounded fixture-backed CMS paging",
	},
	"Tele5IE": {
		rationale: "fixture-backed Aurora CMS recursion preserves public Referer and webpage identity through product extraction",
	},
	"FranceCultureIE": {
		rationale: "fixture-backed public episode extraction with credential-isolated discovery and direct audio product download",
	},
	"RadioFranceIE": {
		rationale: "fixture-backed legacy radiovisions direct audio extraction",
	},
	"RadioFranceLiveIE": {
		rationale: "fixture-backed station and substation live extraction with direct audio and HLS product paths",
	},
	"RadioFrancePodcastIE": {
		rationale: "fixture-backed bounded podcast playlist pagination with transparent child URLs",
	},
	"RadioFranceProfileIE": {
		rationale: "fixture-backed bounded profile playlist pagination with transparent child URLs",
	},
	"RadioFranceProgramScheduleIE": {
		rationale: "fixture-backed program schedule playlists with FranceCulture transparent reentry",
	},
	"VKIE": {
		status:    ExtractorPartiallySupported,
		rationale: "fixture-backed strict anonymous public VK video, clip, embed, vksport alias, signed-query, metadata, and native playback paths; Daxab and external-provider handoffs are outside the current claim",
	},
	"VKPlayIE": {
		rationale: "fixture-backed anonymous VK Play public-recording API with strict vkplay.live, live.vkplay.ru, and live.vkvideo.ru aliases, attributable direct/HLS/DASH playback, and isolated sidecars",
	},
	"VKPlayLiveIE": {
		status:    ExtractorPartiallySupported,
		rationale: "fixture-backed anonymous VK Play live HLS only; live DASH, chat, websocket, authenticated, and account-gated behavior are outside the current claim",
	},
	"VKUserVideosIE": {
		rationale: "fixture-backed public VK user/group video playlists with lazy reusable server-row pagination and repeat-page detection",
	},
	"VKWallPostIE": {
		rationale: "fixture-backed public VK wall posts with bounded audio results, safe VK video embeds, and transparent child reentry",
	},
}

func applyReviewedInventory(class string, entry *ExtractorInventoryEntry) {
	review, ok := reviewedInventory[class]
	if !ok {
		return
	}
	if review.rationale != "" {
		entry.Rationale = review.rationale
	}
	if review.status != "" {
		entry.Status = review.status
	}
}

func validateExtractorInventoryMappings(goIDs map[string]string) error {
	for class, alias := range exactAliases {
		if _, ok := goIDs[normalizeExtractorKey(alias)]; !ok {
			return fmt.Errorf("exact alias %s maps to unregistered Go extractor %q", class, alias)
		}
	}
	for class, review := range reviewedInventory {
		if review.allowUnmapped || review.status == ExtractorPartiallySupported {
			continue
		}
		if _, ok := exactAliases[class]; ok {
			continue
		}
		key := normalizeExtractorKey(strings.TrimSuffix(class, "IE"))
		if _, ok := goIDs[key]; ok {
			continue
		}
		return fmt.Errorf("reviewed inventory class %s has no exact alias or normalized Go key mapping", class)
	}
	return nil
}
