package repoarch

import "testing"

func TestProviderProtocolsKeepDependencyDirection(t *testing.T) {
	for _, test := range []struct {
		source string
		target string
		allow  bool
	}{
		{source: "vectorstores/elasticsearch", target: "vectorstores/protocol/elastic", allow: true},
		{source: "vectorstores/opensearch", target: "vectorstores/protocol/elastic", allow: true},
		{source: "vectorstores/protocol/elastic", target: "core", allow: true},
		{source: "vectorstores/protocol/elastic", target: "vectorstores/elasticsearch"},
		{source: "vectorstores/protocol/elastic", target: "vectorstores/protocol/another"},
		{source: "vectorstores/elasticsearch", target: "vectorstores/opensearch"},
		{source: "vectorstores/elasticsearch", target: "models/protocol/openai"},
		{source: "models/openai", target: "models/protocol/openai", allow: true},
		{source: "models/protocol/openai", target: "core", allow: true},
		{source: "models/protocol/openai", target: "models/openai"},
		{source: "models/protocol/openai", target: "vectorstores/protocol/elastic"},
		{source: "core", target: "vectorstores/protocol/elastic"},
	} {
		t.Run(test.source+"/"+test.target, func(t *testing.T) {
			source := repositoryModule{dir: test.source, layer: moduleLayer(test.source)}
			target := repositoryModule{dir: test.target, layer: moduleLayer(test.target)}
			if allowed := allowedRepositoryDependency(source, target); allowed != test.allow {
				t.Fatalf("dependency %s -> %s allowed = %t, want %t", test.source, test.target, allowed, test.allow)
			}
			if test.allow && source.layer <= target.layer {
				t.Fatalf("dependency %s -> %s does not descend from provider to protocol to core", test.source, test.target)
			}
		})
	}
}
