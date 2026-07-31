package upstreamdelta

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	ExtractorAlreadySupported   = "already_supported"
	ExtractorPartiallySupported = "partially_supported"
	ExtractorExistingBackend    = "uses_existing_shared_backend"
	ExtractorNewBackend         = "requires_new_backend"
	ExtractorAuthOrAntiBot      = "requires_authentication_or_antibot"
	ExtractorObsolete           = "obsolete_or_intentional_deviation"
)

type ExtractorInventoryEntry struct {
	Module        string
	Class         string
	Key           string
	Status        string
	GoExtractor   string
	SharedBackend string
	RiskFlags     string
	Confidence    string
	Rationale     string
}

type ExtractorInventorySummary struct {
	ReferenceCommit string
	Total           int
	Counts          map[string]int
}

var (
	importStartPattern   = regexp.MustCompile(`^from \.([a-zA-Z0-9_]+) import (.+)$`)
	importedClassPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*IE$`)
	classPattern         = regexp.MustCompile(`(?m)^class\s+([A-Za-z][A-Za-z0-9_]*IE)\s*\(`)
	goNamePattern        = regexp.MustCompile(`(?s)func\s*(?:\([^)]*\)\s*)?[A-Za-z0-9_]*Name\(\)\s*string\s*\{.*?return\s+"([a-z0-9_]+)"`)
	configuredKeyPattern = regexp.MustCompile(`newDiscoveryDPlay\(discoveryConfig\{"([a-z0-9_]+)"`)
)

var existingBackendTokens = []struct {
	Name   string
	Tokens []string
}{
	{"anvato", []string{"AnvatoIE", "anvato:"}},
	{"arcpublishing", []string{"ArcPublishingIE", "arcpublishing:"}},
	{"brightcove", []string{"BrightcoveNewIE", "BrightcoveLegacyIE", "players.brightcove.net"}},
	{"cloudflarestream", []string{"CloudflareStreamIE", "videodelivery.net", "cloudflarestream.com"}},
	{"dacast", []string{"DacastVODIE", "dacast.com"}},
	{"jwplatform", []string{"JWPlatformIE", "jwplatform:"}},
	{"kaltura", []string{"KalturaIE", "kaltura:"}},
	{"panopto", []string{"PanoptoIE", "panopto:"}},
	{"sproutvideo", []string{"SproutVideoIE", "sproutvideo:"}},
	{"theplatform", []string{"ThePlatformIE", "ThePlatformFeedIE", "theplatform:"}},
	{"vimeo", []string{"VimeoIE", "vimeo:"}},
	{"wistia", []string{"WistiaIE", "WistiaChannelIE", "wistia:"}},
	{"youtube", []string{"YoutubeIE", "YoutubeTabIE", "youtube:"}},
}

var riskTokens = []struct {
	Name  string
	Token string
}{
	{"login", "_perform_login"},
	{"login", "raise_login_required"},
	{"login", "_NETRC_MACHINE"},
	{"login", "LOGIN_REQUIRED"},
	{"password", "video_password"},
	{"password", "password="},
	{"impersonation", "impersonate="},
	{"authentication", "Authorization"},
	{"authentication", "oauth"},
}

// moduleAliases link upstream modules to Go files that intentionally use a
// different family name. They establish only partial family coverage unless an
// exact extractor-key match also exists.
var moduleAliases = map[string][]string{
	"archiveorg":        {"internetarchive"},
	"applepodcasts":     {"applepodcasts"},
	"ard":               {"ard", "ard_audiothek"},
	"radiofrance":       {"radiofrance", "franceculture", "radiofrance_live", "radiofrance_podcast", "radiofrance_profile", "radiofrance_program_schedule"},
	"bbc":               {"bbciplayer"},
	"svt":               {"region_svt", "region_svt_page"},
	"theweatherchannel": {"weathercom"},
	"youtube":           {"youtube"},
	"acast":             {"podcast"},
	"art19":             {"podcast"},
	"libsyn":            {"podcast"},
	"megaphone":         {"podcast"},
	"simplecast":        {"podcast"},
	"spreaker":          {"podcast"},
	"nrk":               {"nrk", "nrktv", "nrktv_direkte", "nrktv_episode", "nrktv_episodes", "nrktv_season", "nrktv_series", "nrk_radio_podkast", "nrk_skole", "nrk_playlist"},
}

// exactAliases cover intentionally different Go extractor IDs. These mappings
// remain conservative: a class is omitted when the Go implementation only
// covers part of the upstream class's URL corpus.
var exactAliases = map[string]string{
	"AmHistoryChannelIE":           "amhistorychannel",
	"AnimalPlanetIE":               "animalplanet",
	"ArchiveOrgIE":                 "internetarchive",
	"SVTPlayIE":                    "region_svt",
	"SVTSeriesIE":                  "region_svt",
	"SVTPageIE":                    "region_svt",
	"BBCCoUkIE":                    "bbciplayer",
	"ARDBetaMediathekIE":           "ard",
	"ARDAudiothekIE":               "ard_audiothek",
	"ARDAudiothekPlaylistIE":       "ard_audiothek_playlist",
	"FranceCultureIE":              "franceculture",
	"RadioFranceIE":                "radiofrance",
	"RadioFranceLiveIE":            "radiofrance_live",
	"RadioFrancePodcastIE":         "radiofrance_podcast",
	"RadioFranceProfileIE":         "radiofrance_profile",
	"RadioFranceProgramScheduleIE": "radiofrance_program_schedule",
	"ApplePodcastsIE":              "applepodcasts",
	"BandcampAlbumIE":              "bandcamp",
	"BilibiliCollectionListIE":     "bilibili_collection",
	"BilibiliSeriesListIE":         "bilibili_series",
	"TwitchClipsIE":                "twitch_clips",
	"TwitchCollectionIE":           "twitch_collection",
	"TwitchStreamIE":               "twitch_stream",
	"TwitchVideosClipsIE":          "twitch_videos_clips",
	"TwitchVideosCollectionsIE":    "twitch_videos_collections",
	"TwitchVideosIE":               "twitch_videos",
	"TwitchVodIE":                  "twitch_vod",
	"BrightcoveNewIE":              "brightcove",
	"CookingChannelIE":             "cookingchannel",
	"DacastVODIE":                  "dacast",
	"DestinationAmericaIE":         "destinationamerica",
	"DiscoveryLifeIE":              "discoverylife",
	"DiscoveryNetworksDeIE":        "discoverynetworksde",
	"DiscoveryPlusIE":              "discoveryplus",
	"DiscoveryPlusIndiaIE":         "discoveryplusindia",
	"DiscoveryPlusIndiaShowIE":     "discoveryplusindiashow",
	"DiscoveryPlusItalyIE":         "discoveryplusitaly",
	"DiscoveryPlusItalyShowIE":     "discoveryplusitalyshow",
	"DPlayIE":                      "dplay",
	"ImgurAlbumIE":                 "imgur",
	"ImgurGalleryIE":               "imgur",
	"FoodNetworkIE":                "foodnetwork",
	"GoDiscoveryIE":                "godiscovery",
	"HGTVDeIE":                     "hgtvde",
	"HGTVUsaIE":                    "hgtvusa",
	"InvestigationDiscoveryIE":     "investigationdiscovery",
	"KickClipIE":                   "kick",
	"KickVODIE":                    "kick",
	"MixcloudPlaylistIE":           "mixcloud",
	"MixcloudUserIE":               "mixcloud",
	"MicrosoftBuildIE":             "microsoft_build",
	"MicrosoftEmbedIE":             "microsoft_embed",
	"MicrosoftLearnEpisodeIE":      "microsoft_learn_episode",
	"MicrosoftLearnPlaylistIE":     "microsoft_learn_playlist",
	"MicrosoftLearnSessionIE":      "microsoft_learn_session",
	"MicrosoftMediusIE":            "microsoft_medius",
	"NiconicoIE":                   "niconico",
	"NiconicoPlaylistIE":           "niconico_playlist",
	"NiconicoSeriesIE":             "niconico_series",
	"NiconicoUserIE":               "niconico_user",
	"NicovideoSearchIE":            "niconico_search",
	"NicovideoSearchURLIE":         "niconico_search_url",
	"NicovideoTagURLIE":            "niconico_tag",
	"TedEmbedIE":                   "ted_embed",
	"TedPlaylistIE":                "ted_playlist",
	"TedSeriesIE":                  "ted_series",
	"TedTalkIE":                    "ted_talk",
	"RumbleChannelIE":              "rumble",
	"RumbleEmbedIE":                "rumble",
	"ScienceChannelIE":             "sciencechannel",
	"TLCIE":                        "tlc",
	"Tele5IE":                      "tele5",
	"TravelChannelIE":              "travelchannel",
	"TVAIE":                        "tva",
}

func BuildExtractorInventory(referenceRoot, repositoryRoot string) ([]ExtractorInventoryEntry, error) {
	registered, err := parseRegisteredExtractors(filepath.Join(referenceRoot, "yt_dlp", "extractor", "_extractors.py"))
	if err != nil {
		return nil, err
	}
	goIDs, goModules, err := parseGoExtractorInventory(filepath.Join(repositoryRoot, "internal", "extractor"))
	if err != nil {
		return nil, err
	}
	if err := validateExtractorInventoryMappings(goIDs); err != nil {
		return nil, err
	}

	entries := make([]ExtractorInventoryEntry, 0, len(registered))
	moduleCache := make(map[string]string)
	for _, item := range registered {
		source, ok := moduleCache[item.Module]
		if !ok {
			source, err = readUpstreamModule(filepath.Join(referenceRoot, "yt_dlp", "extractor"), item.Module)
			if err != nil {
				return nil, err
			}
			moduleCache[item.Module] = source
		}
		block := classBlock(source, item.Class)
		entry := classifyExtractor(item.Module, item.Class, block, goIDs, goModules)
		applyReviewedInventory(item.Class, &entry)
		if entry.Status == ExtractorAlreadySupported {
			if _, ok := goIDs[normalizeExtractorKey(entry.GoExtractor)]; !ok {
				return nil, fmt.Errorf("classified %s as supported without registered Go extractor %q", item.Class, entry.GoExtractor)
			}
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Module == entries[j].Module {
			return entries[i].Class < entries[j].Class
		}
		return entries[i].Module < entries[j].Module
	})
	return entries, nil
}

func readUpstreamModule(root, module string) (string, error) {
	filePath := filepath.Join(root, module+".py")
	if body, err := os.ReadFile(filePath); err == nil {
		return string(body), nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read upstream extractor %s: %w", module, err)
	}

	moduleRoot := filepath.Join(root, module)
	var paths []string
	err := filepath.WalkDir(moduleRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".py") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("read upstream extractor package %s: %w", module, err)
	}
	sort.Strings(paths)
	var source strings.Builder
	for _, path := range paths {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("read upstream extractor package file %s: %w", path, readErr)
		}
		source.Write(body)
		source.WriteString("\n")
	}
	if source.Len() == 0 {
		return "", fmt.Errorf("upstream extractor module %s has no Python source", module)
	}
	return source.String(), nil
}

type registeredExtractor struct {
	Module string
	Class  string
}

func parseRegisteredExtractors(path string) ([]registeredExtractor, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open extractor registry: %w", err)
	}
	defer file.Close()

	var result []registeredExtractor
	var activeModule string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if activeModule != "" {
			if line == ")" {
				activeModule = ""
				continue
			}
			if class := importedClass(line); class != "" {
				result = append(result, registeredExtractor{Module: activeModule, Class: class})
			}
			continue
		}
		match := importStartPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		module, imported := match[1], strings.TrimSpace(match[2])
		if imported == "(" {
			activeModule = module
			continue
		}
		if class := importedClass(imported); class != "" {
			result = append(result, registeredExtractor{Module: module, Class: class})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan extractor registry: %w", err)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("extractor registry yielded no classes")
	}
	return result, nil
}

func importedClass(line string) string {
	line = strings.TrimSpace(strings.TrimSuffix(strings.SplitN(line, "#", 2)[0], ","))
	if strings.HasSuffix(line, "IE") && importedClassPattern.MatchString(line) {
		return line
	}
	return ""
}

func parseGoExtractorInventory(root string) (map[string]string, map[string]bool, error) {
	ids := make(map[string]string)
	modules := make(map[string]bool)
	paths, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		return nil, nil, err
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, nil, readErr
		}
		module := strings.TrimSuffix(filepath.Base(path), ".go")
		modules[module] = true
		for _, match := range goNamePattern.FindAllStringSubmatch(string(body), -1) {
			id := match[1]
			ids[normalizeExtractorKey(id)] = id
		}
		if module == "dplay" {
			for _, match := range configuredKeyPattern.FindAllStringSubmatch(string(body), -1) {
				id := match[1]
				ids[normalizeExtractorKey(id)] = id
			}
		}
	}
	return ids, modules, nil
}

func classifyExtractor(module, class, block string, goIDs map[string]string, goModules map[string]bool) ExtractorInventoryEntry {
	entry := ExtractorInventoryEntry{
		Module:        module,
		Class:         class,
		Key:           strings.TrimSuffix(class, "IE"),
		SharedBackend: detectSharedBackend(block),
		RiskFlags:     detectRiskFlags(block),
	}
	if strings.Contains(block, "_WORKING = False") {
		entry.Status = ExtractorObsolete
		entry.Confidence = "high"
		entry.Rationale = "upstream class explicitly declares _WORKING = False"
		return entry
	}
	if alias, ok := exactAliases[class]; ok {
		entry.Status = ExtractorAlreadySupported
		entry.GoExtractor = alias
		entry.Confidence = "high"
		entry.Rationale = "curated exact mapping to a registered Go extractor"
		return entry
	}
	if goID, ok := goIDs[normalizeExtractorKey(entry.Key)]; ok {
		entry.Status = ExtractorAlreadySupported
		entry.GoExtractor = goID
		entry.Confidence = "high"
		entry.Rationale = "normalized upstream key exactly matches a registered Go extractor"
		return entry
	}
	if moduleImplemented(module, goModules) {
		entry.Status = ExtractorPartiallySupported
		entry.Confidence = "medium"
		entry.Rationale = "the Go port implements this upstream site family, but no exact extractor-key mapping is proven"
		return entry
	}
	if entry.RiskFlags != "" {
		entry.Status = ExtractorAuthOrAntiBot
		entry.Confidence = "medium"
		entry.Rationale = "upstream class contains explicit authentication, password, OAuth, or impersonation behavior"
		return entry
	}
	if entry.SharedBackend != "" {
		entry.Status = ExtractorExistingBackend
		entry.Confidence = "medium"
		entry.Rationale = "upstream class references a media backend already implemented in Go"
		return entry
	}
	entry.Status = ExtractorNewBackend
	entry.Confidence = "low"
	entry.Rationale = "no exact Go mapping or existing shared-backend handoff was detected; manual family review is required"
	return entry
}

func moduleImplemented(module string, goModules map[string]bool) bool {
	if goModules[module] {
		return true
	}
	for _, alias := range moduleAliases[module] {
		if goModules[alias] {
			return true
		}
	}
	return false
}

func detectRiskFlags(block string) string {
	seen := make(map[string]bool)
	var flags []string
	for _, candidate := range riskTokens {
		if strings.Contains(strings.ToLower(block), strings.ToLower(candidate.Token)) && !seen[candidate.Name] {
			seen[candidate.Name] = true
			flags = append(flags, candidate.Name)
		}
	}
	sort.Strings(flags)
	return strings.Join(flags, ";")
}

func detectSharedBackend(block string) string {
	for _, backend := range existingBackendTokens {
		for _, token := range backend.Tokens {
			if strings.Contains(block, token) {
				return backend.Name
			}
		}
	}
	return ""
}

func classBlock(source, class string) string {
	match := regexp.MustCompile(`(?m)^class\s+` + regexp.QuoteMeta(class) + `\s*\(`).FindStringIndex(source)
	if match == nil {
		return ""
	}
	rest := source[match[0]:]
	next := classPattern.FindStringIndex(rest[len("class "):])
	if next == nil {
		return rest
	}
	return rest[:len("class ")+next[0]]
}

func normalizeExtractorKey(value string) string {
	var result strings.Builder
	for _, char := range strings.ToLower(value) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func WriteExtractorInventoryCSV(writer io.Writer, entries []ExtractorInventoryEntry) error {
	output := csv.NewWriter(writer)
	if err := output.Write([]string{
		"module", "class", "extractor_key", "status", "go_extractor",
		"shared_backend", "risk_flags", "confidence", "rationale",
	}); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := output.Write([]string{
			entry.Module, entry.Class, entry.Key, entry.Status, entry.GoExtractor,
			entry.SharedBackend, entry.RiskFlags, entry.Confidence, entry.Rationale,
		}); err != nil {
			return err
		}
	}
	output.Flush()
	return output.Error()
}

func SummarizeExtractorInventory(referenceCommit string, entries []ExtractorInventoryEntry) ExtractorInventorySummary {
	summary := ExtractorInventorySummary{
		ReferenceCommit: referenceCommit,
		Total:           len(entries),
		Counts:          make(map[string]int),
	}
	for _, entry := range entries {
		summary.Counts[entry.Status]++
	}
	return summary
}
