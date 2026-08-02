package cli

import "testing"

func TestAdaptiveStreamingCLIFlagsAndNegativeAliases(t *testing.T) {
	request := captureCLIRequest(t, "--hls-split-discontinuity", "--no-hls-split-discontinuity", "--hls-split-discontinuity")
	if !request.HLSSplitDiscontinuity {
		t.Fatalf("HLS request=%+v", request)
	}
	request = captureCLIRequest(t, "--no-hls-split-discontinuity")
	if request.HLSSplitDiscontinuity {
		t.Fatalf("negative HLS alias did not disable split: %+v", request)
	}
	request = captureCLIRequest(t)
	if request.DenyDynamicMPD {
		t.Fatalf("dynamic MPD defaults denied: %+v", request)
	}
	request = captureCLIRequest(t, "--no-allow-dynamic-mpd")
	if !request.DenyDynamicMPD {
		t.Fatalf("--no-allow-dynamic-mpd request=%+v", request)
	}
	request = captureCLIRequest(t, "--ignore-dynamic-mpd")
	if !request.DenyDynamicMPD {
		t.Fatalf("--ignore-dynamic-mpd request=%+v", request)
	}
	request = captureCLIRequest(t, "--no-allow-dynamic-mpd", "--allow-dynamic-mpd")
	if request.DenyDynamicMPD {
		t.Fatalf("last dynamic MPD alias did not win: %+v", request)
	}
}
