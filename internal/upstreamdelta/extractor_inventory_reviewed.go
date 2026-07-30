package upstreamdelta

import (
	"fmt"
	"strings"
)

// reviewedInventory preserves fixture-backed or otherwise reviewed rationale
// text for rows whose classification is already determined by exactAliases or
// normalized Go key matches. Overrides are keyed only by upstream IE class
// names that exist in the pinned registry; they cannot invent rows or promote
// unsupported classes.
type reviewedInventoryEntry struct {
	rationale string
	status    string
}

var reviewedInventory = map[string]reviewedInventoryEntry{
	"DailymotionPlaylistIE": {
		status:    ExtractorPartiallySupported,
		rationale: "the Go port implements playlist metadata via player metadata rather than GraphQL collection pagination",
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
		if review.status == ExtractorPartiallySupported {
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
