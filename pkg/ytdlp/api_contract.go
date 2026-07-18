package ytdlp

// APIVersion identifies the supported public Go contract. The v1alpha1
// contract follows the compatibility rules documented in
// docs/P2_API_COMPATIBILITY_POLICY.md.
const APIVersion = "v1alpha1"

// CompatibilityReferenceCommit identifies the behavioral baseline used for
// attributable compatibility fixtures. It is metadata only: the product does
// not read or execute the reference checkout.
const CompatibilityReferenceCommit = "aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8"

const (
	EventBrowserCookies       = "browser_cookies"
	EventExtracting           = "extracting"
	EventExtracted            = "extracted"
	EventDownloadStarting     = "download_starting"
	EventDownloadProgress     = "download_progress"
	EventDownloadRetry        = "download_retry"
	EventDownloadCancelled    = "download_cancelled"
	EventDownloadCompleted    = "download_completed"
	EventFragmentStarting     = "fragment_starting"
	EventFragmentCompleted    = "fragment_completed"
	EventPostprocessStarting  = "postprocess_starting"
	EventPostprocessProgress  = "postprocess_progress"
	EventPostprocessCompleted = "postprocess_completed"
)
