// Package ytdlp is the broad compatibility facade. Provider-neutral client
// orchestration lives in engine; NewClient explicitly supplies this package's
// complete first-party catalog and provider-specific support hooks.
package ytdlp

import (
	"context"
	"crypto/ed25519"
	"io"
	"math/rand"
	"time"

	"github.com/tejasa97/ytdlp-go/engine"
)

type (
	Client                            = engine.Client
	Runner                            = engine.Runner
	Request                           = engine.Request
	EJSPreprocessedPlayerCacheOptions = engine.EJSPreprocessedPlayerCacheOptions
	Result                            = engine.Result
	Option                            = engine.Option

	Error                   = engine.Error
	ErrorCategory           = engine.ErrorCategory
	DownloadHTTPStatusError = engine.DownloadHTTPStatusError
	Event                   = engine.Event
	EventHandler            = engine.EventHandler
	Artifact                = engine.Artifact
	Chapter                 = engine.Chapter

	MetadataAction               = engine.MetadataAction
	MetadataActionKind           = engine.MetadataActionKind
	FormatCheckMode              = engine.FormatCheckMode
	StopKind                     = engine.StopKind
	InteractiveMatchFilterPrompt = engine.InteractiveMatchFilterPrompt
	InteractiveMatchFilterFunc   = engine.InteractiveMatchFilterFunc
	InteractiveFormatPrompt      = engine.InteractiveFormatPrompt
	InteractiveFormatFunc        = engine.InteractiveFormatFunc

	ExtractorSelectionOptions = engine.ExtractorSelectionOptions
	DownloaderOptions         = engine.DownloaderOptions
	ExternalDownloader        = engine.ExternalDownloader
	SimpleFilterOptions       = engine.SimpleFilterOptions
	SubtitleOptions           = engine.SubtitleOptions
	RelatedFileOptions        = engine.RelatedFileOptions
	FilesystemOptions         = engine.FilesystemOptions
	ResumeOptions             = engine.ResumeOptions
	CommitTarget              = engine.CommitTarget
	ArtifactKind              = engine.ArtifactKind
	OutputRootRef             = engine.OutputRootRef
	ResumeSummary             = engine.ResumeSummary
	ResumeComponent           = engine.ResumeComponent
	ResumeInspectionClass     = engine.ResumeInspectionClass
	ResumeDiscardDisposition  = engine.ResumeDiscardDisposition
	ResumeDiscardHandle       = engine.ResumeDiscardHandle
	ResumeDiscardResult       = engine.ResumeDiscardResult
	CollectionResult          = engine.CollectionResult
	OutputPreviewRequest      = engine.OutputPreviewRequest
	ArtifactDeclaration       = engine.ArtifactDeclaration
	SessionOutcome            = engine.SessionOutcome
	SessionPhase              = engine.SessionPhase
	SessionStatus             = engine.SessionStatus
	SessionDesiredState       = engine.SessionDesiredState
	SessionPublicationState   = engine.SessionPublicationState
	SessionCleanupState       = engine.SessionCleanupState
	SessionDisposition        = engine.SessionDisposition
	PublicationOutcome        = engine.PublicationOutcome
	CleanupOutcome            = engine.CleanupOutcome
	ThumbnailOptions          = engine.ThumbnailOptions
	YouTubeCommentOptions     = engine.YouTubeCommentOptions
	SoundCloudCommentOptions  = engine.SoundCloudCommentOptions
	SponsorBlockOptions       = engine.SponsorBlockOptions
	NHKOptions                = engine.NHKOptions
	PlaylistOptions           = engine.PlaylistOptions
	PlaylistErrorPolicy       = engine.PlaylistErrorPolicy
	PlaylistRandomSource      = engine.PlaylistRandomSource

	OutputPathType     = engine.OutputPathType
	OutputPaths        = engine.OutputPaths
	OutputTemplateType = engine.OutputTemplateType
	OutputTemplates    = engine.OutputTemplates
	PrintStage         = engine.PrintStage
	PrintRule          = engine.PrintRule
	PrintOutput        = engine.PrintOutput

	Postprocessor                 = engine.Postprocessor
	MovePostprocessor             = engine.MovePostprocessor
	RemuxPostprocessor            = engine.RemuxPostprocessor
	RecodeVideoPostprocessor      = engine.RecodeVideoPostprocessor
	ExtractAudioPostprocessor     = engine.ExtractAudioPostprocessor
	EmbedSubtitlePostprocessor    = engine.EmbedSubtitlePostprocessor
	EmbedThumbnailPostprocessor   = engine.EmbedThumbnailPostprocessor
	EmbedMetadataPostprocessor    = engine.EmbedMetadataPostprocessor
	EmbedChaptersPostprocessor    = engine.EmbedChaptersPostprocessor
	ConvertSubtitlePostprocessor  = engine.ConvertSubtitlePostprocessor
	ConvertThumbnailPostprocessor = engine.ConvertThumbnailPostprocessor
	FixupPostprocessor            = engine.FixupPostprocessor
	ConcatPostprocessor           = engine.ConcatPostprocessor

	TelemetryOutcome   = engine.TelemetryOutcome
	TelemetryCount     = engine.TelemetryCount
	TelemetryOverflow  = engine.TelemetryOverflow
	TelemetrySnapshot  = engine.TelemetrySnapshot
	TelemetryCoverage  = engine.TelemetryCoverage
	TelemetryConfig    = engine.TelemetryConfig
	TelemetryCollector = engine.TelemetryCollector

	PluginPermission            = engine.PluginPermission
	PluginCapability            = engine.PluginCapability
	PluginManifest              = engine.PluginManifest
	PluginApprovalRequest       = engine.PluginApprovalRequest
	PluginApproval              = engine.PluginApproval
	PluginExtractRequest        = engine.PluginExtractRequest
	PluginExtractResponse       = engine.PluginExtractResponse
	PluginPostprocessRequest    = engine.PluginPostprocessRequest
	PluginPostprocessResponse   = engine.PluginPostprocessResponse
	PluginProviderRequest       = engine.PluginProviderRequest
	PluginProviderResponse      = engine.PluginProviderResponse
	PluginPermissionApprover    = engine.PluginPermissionApprover
	PluginPermissionApproveFunc = engine.PluginPermissionApproveFunc
	PluginLimits                = engine.PluginLimits
	PluginSandbox               = engine.PluginSandbox
	PackRevocation              = engine.PackRevocation
	PackRevocations             = engine.PackRevocations
	PackPermissionReview        = engine.PackPermissionReview
	PackState                   = engine.PackState
	PluginPackTrust             = engine.PluginPackTrust
	PluginPackInstallOptions    = engine.PluginPackInstallOptions
	PluginDescriptor            = engine.PluginDescriptor
	InstalledPlugin             = engine.InstalledPlugin
	PluginPackRollbackOptions   = engine.PluginPackRollbackOptions
	PluginPackRemoveOptions     = engine.PluginPackRemoveOptions
	PluginHost                  = engine.PluginHost
)

const (
	APIVersion                   = engine.APIVersion
	CompatibilityReferenceCommit = engine.CompatibilityReferenceCommit

	ErrorUnsupported    = engine.ErrorUnsupported
	ErrorAuthentication = engine.ErrorAuthentication
	ErrorInvalidInput   = engine.ErrorInvalidInput
	ErrorNetwork        = engine.ErrorNetwork
	ErrorSecurity       = engine.ErrorSecurity
	ErrorCancelled      = engine.ErrorCancelled
	ErrorInternal       = engine.ErrorInternal

	EventBrowserCookies       = engine.EventBrowserCookies
	EventExtracting           = engine.EventExtracting
	EventExtracted            = engine.EventExtracted
	EventDownloadStarting     = engine.EventDownloadStarting
	EventDownloadProgress     = engine.EventDownloadProgress
	EventDownloadRetry        = engine.EventDownloadRetry
	EventExtractorRetry       = engine.EventExtractorRetry
	EventDownloadCancelled    = engine.EventDownloadCancelled
	EventDownloadCompleted    = engine.EventDownloadCompleted
	EventFragmentStarting     = engine.EventFragmentStarting
	EventFragmentCompleted    = engine.EventFragmentCompleted
	EventPostprocessStarting  = engine.EventPostprocessStarting
	EventPostprocessProgress  = engine.EventPostprocessProgress
	EventPostprocessCompleted = engine.EventPostprocessCompleted
	EventArchiveMatch         = engine.EventArchiveMatch
	EventMetadataWarning      = engine.EventMetadataWarning
	EventMatchFilterSkipped   = engine.EventMatchFilterSkipped
	EventJavaScriptChallenge  = engine.EventJavaScriptChallenge

	MetadataActionParse   = engine.MetadataActionParse
	MetadataActionReplace = engine.MetadataActionReplace
	FormatCheckAuto       = engine.FormatCheckAuto
	FormatCheckNone       = engine.FormatCheckNone
	FormatCheckSelected   = engine.FormatCheckSelected
	FormatCheckAll        = engine.FormatCheckAll
	StopNone              = engine.StopNone
	StopBreakMatchFilter  = engine.StopBreakMatchFilter
	StopBreakOnReject     = engine.StopBreakOnReject
	StopBreakOnExisting   = engine.StopBreakOnExisting
	StopMaxDownloads      = engine.StopMaxDownloads

	OutputPathHome          = engine.OutputPathHome
	OutputPathSubtitle      = engine.OutputPathSubtitle
	OutputPathThumbnail     = engine.OutputPathThumbnail
	OutputPathDescription   = engine.OutputPathDescription
	OutputPathInfoJSON      = engine.OutputPathInfoJSON
	OutputPathLink          = engine.OutputPathLink
	OutputPathPLDescription = engine.OutputPathPLDescription
	OutputPathPLInfoJSON    = engine.OutputPathPLInfoJSON
	OutputPathPLThumbnail   = engine.OutputPathPLThumbnail
	OutputPathPLVideo       = engine.OutputPathPLVideo
	OutputPathChapter       = engine.OutputPathChapter

	OutputTemplateDefault               = engine.OutputTemplateDefault
	OutputTemplateSubtitle              = engine.OutputTemplateSubtitle
	OutputTemplateThumbnail             = engine.OutputTemplateThumbnail
	OutputTemplateDescription           = engine.OutputTemplateDescription
	OutputTemplateInfoJSON              = engine.OutputTemplateInfoJSON
	OutputTemplateLink                  = engine.OutputTemplateLink
	OutputTemplatePLDescription         = engine.OutputTemplatePLDescription
	OutputTemplatePLInfoJSON            = engine.OutputTemplatePLInfoJSON
	OutputTemplatePLThumbnail           = engine.OutputTemplatePLThumbnail
	OutputTemplatePLVideo               = engine.OutputTemplatePLVideo
	OutputTemplateChapter               = engine.OutputTemplateChapter
	ArtifactKindPrimary                 = engine.ArtifactKindPrimary
	ResumeDiscarded                     = engine.ResumeDiscarded
	ResumeDiscardCleanupPending         = engine.ResumeDiscardCleanupPending
	ResumeDiscardReconciliationRequired = engine.ResumeDiscardReconciliationRequired
	SessionRetained                     = engine.SessionRetained
	SessionDiscarded                    = engine.SessionDiscarded
	SessionCleanupPending               = engine.SessionCleanupPending
	SessionCollision                    = engine.SessionCollision
	SessionPublished                    = engine.SessionPublished
	SessionRecoveryRequired             = engine.SessionRecoveryRequired
	PublicationNotAttempted             = engine.PublicationNotAttempted
	PublicationReady                    = engine.PublicationReady
	PublicationWon                      = engine.PublicationWon
	PublicationCollision                = engine.PublicationCollision
	PublicationIndeterminateOutcome     = engine.PublicationIndeterminateOutcome
	CleanupNotNeeded                    = engine.CleanupNotNeeded
	CleanupComplete                     = engine.CleanupComplete
	CleanupPendingOutcome               = engine.CleanupPendingOutcome
	CleanupRecoveryNeeded               = engine.CleanupRecoveryNeeded

	PrintPreProcess  = engine.PrintPreProcess
	PrintAfterFilter = engine.PrintAfterFilter
	PrintVideo       = engine.PrintVideo
	PrintBeforeDL    = engine.PrintBeforeDL
	PrintPostProcess = engine.PrintPostProcess
	PrintAfterMove   = engine.PrintAfterMove
	PrintAfterVideo  = engine.PrintAfterVideo
	PrintPlaylist    = engine.PrintPlaylist

	PlaylistErrorContinue    = engine.PlaylistErrorContinue
	PlaylistErrorAbort       = engine.PlaylistErrorAbort
	FixupNever               = engine.FixupNever
	FixupIgnore              = engine.FixupIgnore
	FixupWarn                = engine.FixupWarn
	FixupDetectOrWarn        = engine.FixupDetectOrWarn
	FixupForce               = engine.FixupForce
	ConcatPlaylistNever      = engine.ConcatPlaylistNever
	ConcatPlaylistAlways     = engine.ConcatPlaylistAlways
	ConcatPlaylistMultiVideo = engine.ConcatPlaylistMultiVideo

	TelemetryUnknownExtractor       = engine.TelemetryUnknownExtractor
	TelemetryCapabilityExtract      = engine.TelemetryCapabilityExtract
	TelemetryOutcomeSuccess         = engine.TelemetryOutcomeSuccess
	TelemetryOutcomeError           = engine.TelemetryOutcomeError
	TelemetryOutcomeFallback        = engine.TelemetryOutcomeFallback
	TelemetryOutcomeUnsupported     = engine.TelemetryOutcomeUnsupported
	PluginPermissionNetwork         = engine.PluginPermissionNetwork
	PluginPermissionCookies         = engine.PluginPermissionCookies
	PluginPermissionSecrets         = engine.PluginPermissionSecrets
	PluginPermissionFilesystemRead  = engine.PluginPermissionFilesystemRead
	PluginPermissionFilesystemWrite = engine.PluginPermissionFilesystemWrite
	PluginPermissionProcess         = engine.PluginPermissionProcess
)

var (
	ErrUnsupported                       = engine.ErrUnsupported
	ErrInvalidRouting                    = engine.ErrInvalidRouting
	ErrUnsupportedRouting                = engine.ErrUnsupportedRouting
	ErrInvalidMetadata                   = engine.ErrInvalidMetadata
	ErrUnavailable                       = engine.ErrUnavailable
	ErrRegionRestricted                  = engine.ErrRegionRestricted
	ErrAuthentication                    = engine.ErrAuthentication
	ErrWrongPassword                     = engine.ErrWrongPassword
	ErrChallengeSolver                   = engine.ErrChallengeSolver
	ErrTransportProfile                  = engine.ErrTransportProfile
	ErrTransportIsolation                = engine.ErrTransportIsolation
	ErrInvalidPlaylist                   = engine.ErrInvalidPlaylist
	ErrPlaylistLimit                     = engine.ErrPlaylistLimit
	ErrInvalidSelection                  = engine.ErrInvalidSelection
	ErrSelectionDisabled                 = engine.ErrSelectionDisabled
	ErrInteractiveInput                  = engine.ErrInteractiveInput
	ErrFormatCheckLimit                  = engine.ErrFormatCheckLimit
	ErrInvalidInfoJSON                   = engine.ErrInvalidInfoJSON
	ErrXattrsUnsupported                 = engine.ErrXattrsUnsupported
	ErrHLSDiscontinuitySelection         = engine.ErrHLSDiscontinuitySelection
	ErrHLSDiscontinuityGroupMissing      = engine.ErrHLSDiscontinuityGroupMissing
	ErrHLSDiscontinuityPlaylistEmpty     = engine.ErrHLSDiscontinuityPlaylistEmpty
	ErrHLSDiscontinuityGroupAdOnly       = engine.ErrHLSDiscontinuityGroupAdOnly
	ErrHLSDiscontinuityPlaylistMalformed = engine.ErrHLSDiscontinuityPlaylistMalformed
	ErrHLSDiscontinuityHostPolicy        = engine.ErrHLSDiscontinuityHostPolicy
	ErrPauseRequested                    = engine.ErrPauseRequested
	ErrDestinationCollision              = engine.ErrDestinationCollision
	ErrResumeIdentityMismatch            = engine.ErrResumeIdentityMismatch
	ErrResumeIdentityRequired            = engine.ErrResumeIdentityRequired
	ErrSessionNeedsReconciliation        = engine.ErrSessionNeedsReconciliation
	ErrSessionInUse                      = engine.ErrSessionInUse

	MergeOutputFormatSupported = engine.MergeOutputFormatSupported
)

func NewClient(options ...Option) *Client {
	return engine.NewClient(broadCompatibilityComposition(), options...)
}

func BuiltInExtractorIDs() []string { return productRegistry().Names() }

func ValidateOutputRoot(path string) (OutputRootRef, error) { return engine.ValidateOutputRoot(path) }
func InspectResumeState(ctx context.Context, root OutputRootRef, sessionID string) (ResumeSummary, error) {
	return engine.InspectResumeState(ctx, root, sessionID)
}
func PrepareResumeDiscard(ctx context.Context, root OutputRootRef, sessionID string) (*ResumeDiscardHandle, error) {
	return engine.PrepareResumeDiscard(ctx, root, sessionID)
}
func CollectResumeOrphans(ctx context.Context, root OutputRootRef, live map[string]struct{}, olderThan time.Time) (CollectionResult, error) {
	return engine.CollectResumeOrphans(ctx, root, live, olderThan)
}
func RenderOutputArtifacts(request OutputPreviewRequest) ([]ArtifactDeclaration, error) {
	return engine.RenderOutputArtifacts(request)
}

func WithEventHandler(handler EventHandler) Option { return engine.WithEventHandler(handler) }
func WithJavaScriptHelper(path string) Option      { return engine.WithJavaScriptHelper(path) }
func WithEJSPreprocessedPlayerCache(options EJSPreprocessedPlayerCacheOptions) Option {
	return engine.WithEJSPreprocessedPlayerCache(options)
}
func WithTelemetryCollector(collector *TelemetryCollector) Option {
	return engine.WithTelemetryCollector(collector)
}
func WithInstalledPlugins(installed ...*InstalledPlugin) Option {
	return engine.WithInstalledPlugins(installed...)
}
func WithPluginPermissionApprover(approver PluginPermissionApprover) Option {
	return engine.WithPluginPermissionApprover(approver)
}
func IsCategory(err error, category ErrorCategory) bool { return engine.IsCategory(err, category) }
func DownloadHTTPStatusCode(err error) (int, bool)      { return engine.DownloadHTTPStatusCode(err) }
func IsNonOverridableError(err error) bool              { return engine.IsNonOverridableError(err) }
func ParseMergeOutputFormat(explicit string) ([]string, error) {
	return engine.ParseMergeOutputFormat(explicit)
}
func DefaultPlaylistRandomSource() *rand.Rand { return engine.DefaultPlaylistRandomSource() }
func NewTelemetryCollector(config TelemetryConfig) (*TelemetryCollector, error) {
	return engine.NewTelemetryCollector(config)
}
func DecodeTelemetrySnapshot(ctx context.Context, reader io.Reader, maxBytes int64) (TelemetrySnapshot, error) {
	return engine.DecodeTelemetrySnapshot(ctx, reader, maxBytes)
}
func PluginPackKeyID(key ed25519.PublicKey) (string, error) { return engine.PluginPackKeyID(key) }
func VerifyPluginPack(archive []byte, trust PluginPackTrust) (PluginDescriptor, error) {
	return engine.VerifyPluginPack(archive, trust)
}
func InstallPluginPack(ctx context.Context, archive []byte, root string, trust PluginPackTrust, options PluginPackInstallOptions) (*InstalledPlugin, PackPermissionReview, error) {
	return engine.InstallPluginPack(ctx, archive, root, trust, options)
}
func RollbackPluginPack(ctx context.Context, root, name string, trust PluginPackTrust, options PluginPackRollbackOptions) (*InstalledPlugin, PackPermissionReview, error) {
	return engine.RollbackPluginPack(ctx, root, name, trust, options)
}
func RemovePluginPack(ctx context.Context, root, name, version string, trust PluginPackTrust, options PluginPackRemoveOptions) (PackState, PackPermissionReview, error) {
	return engine.RemovePluginPack(ctx, root, name, version, trust, options)
}
func NewSandboxedPluginHost(installed *InstalledPlugin, approver PluginPermissionApprover, limits PluginLimits, policy PluginSandbox) (*PluginHost, error) {
	return engine.NewSandboxedPluginHost(installed, approver, limits, policy)
}
func NewPluginHost(installed *InstalledPlugin, approver PluginPermissionApprover, limits PluginLimits) (*PluginHost, error) {
	return engine.NewPluginHost(installed, approver, limits)
}
