package cli

import "testing"

func TestRunAddressPolicyFlagsUseLastOptionWins(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		source    string
		forceIPv4 bool
		forceIPv6 bool
	}{
		{name: "source then ipv4", args: []string{"--source-address", "127.0.0.1", "--force-ipv4"}, forceIPv4: true},
		{name: "ipv4 then source", args: []string{"--force-ipv4", "--source-address", "127.0.0.1"}, source: "127.0.0.1"},
		{name: "ipv4 then ipv6", args: []string{"--force-ipv4", "--force-ipv6"}, forceIPv6: true},
		{name: "short aliases", args: []string{"-4", "-6"}, forceIPv6: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := captureCLIRequest(t, append(test.args, "--extractor-retries", "7")...)
			if request.SourceAddress != test.source || request.ForceIPv4 != test.forceIPv4 || request.ForceIPv6 != test.forceIPv6 {
				t.Fatalf("address policy = source %q, ipv4 %v, ipv6 %v", request.SourceAddress, request.ForceIPv4, request.ForceIPv6)
			}
			if request.ExtractorRetries != 7 {
				t.Fatalf("extractor retries = %d, want 7", request.ExtractorRetries)
			}
		})
	}
}

func TestRunExtractorRetriesDefaultsToThreeAndAllowsZero(t *testing.T) {
	request := captureCLIRequest(t)
	if request.ExtractorRetries != 3 {
		t.Fatalf("default extractor retries = %d, want 3", request.ExtractorRetries)
	}
	request = captureCLIRequest(t, "--extractor-retries", "0")
	if request.ExtractorRetries != 0 {
		t.Fatalf("zero extractor retries = %d, want 0", request.ExtractorRetries)
	}
	request = captureCLIRequest(t, "--extractor-retries", "100")
	if request.ExtractorRetries != 100 {
		t.Fatalf("maximum extractor retries = %d, want 100", request.ExtractorRetries)
	}
}
