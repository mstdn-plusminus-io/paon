package telemetry

import (
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestOptionsFromConfigCarriesVCSResourceAttributes(t *testing.T) {
	options := OptionsFromConfig(config.Config{
		Version:      "4.4.22+paon",
		SourceCommit: "0123456789abcdef",
		SourceURL:    "https://github.com/mstdn-plusminus-io/paon",
	}, "web")
	if options.SourceCommit != "0123456789abcdef" || options.SourceURL != "https://github.com/mstdn-plusminus-io/paon" {
		t.Fatalf("VCS options = %#v", options)
	}
	attributes := telemetryResourceAttributes(options)
	values := make(map[string]string, len(attributes))
	for _, item := range attributes {
		values[string(item.Key)] = item.Value.AsString()
	}
	if values["vcs.repository.ref.revision"] != options.SourceCommit || values["vcs.repository.url.full"] != options.SourceURL {
		t.Fatalf("VCS resource attributes = %#v", values)
	}
}
