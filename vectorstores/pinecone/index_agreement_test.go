package pinecone

import (
	"errors"
	"net/http"
	"testing"

	"github.com/pinecone-io/go-pinecone/v4/pinecone"
)

// The metric decides what a raw Pinecone score means, and the store used to
// take the caller's word for it -- the config even told them to go read the
// value from DescribeIndex, while the store held the client that could read it.
// A wrong metric produces plausible ranked output that is wrong: cosine reads
// an unbounded inner product as a similarity, and MinScore filters by the wrong
// direction. Nothing downstream can notice.
func TestValidateIndexMetricRefusesADisagreeingIndex(t *testing.T) {
	t.Parallel()

	indexes := []*pinecone.Index{
		{Name: "other", Host: "other-abc.svc.us-east1-aws.pinecone.io", Metric: pinecone.Cosine},
		{Name: "docs", Host: "docs-abc.svc.us-east1-aws.pinecone.io", Metric: pinecone.Dotproduct},
	}

	err := validateIndexMetric(indexes, "docs-abc.svc.us-east1-aws.pinecone.io", DistanceCosine)
	if !errors.Is(err, ErrIncompatibleIndex) {
		t.Fatalf("validateIndexMetric() = %v, want ErrIncompatibleIndex", err)
	}
	if err = validateIndexMetric(indexes, "docs-abc.svc.us-east1-aws.pinecone.io", DistanceDot); err != nil {
		t.Fatalf("validateIndexMetric() = %v, want nil for the index's own metric", err)
	}
}

// A host nothing serves is the other half of the same question: the store would
// otherwise build a connection and fail on the first query, away from the
// wiring that is actually wrong.
func TestValidateIndexMetricRefusesAnUnservedHost(t *testing.T) {
	t.Parallel()

	indexes := []*pinecone.Index{
		{Name: "docs", Host: "docs-abc.svc.us-east1-aws.pinecone.io", Metric: pinecone.Cosine},
	}

	err := validateIndexMetric(indexes, "typo-abc.svc.us-east1-aws.pinecone.io", DistanceCosine)
	if !errors.Is(err, ErrIncompatibleIndex) {
		t.Fatalf("validateIndexMetric() = %v, want ErrIncompatibleIndex", err)
	}
}

// IndexHost is documented as a host URL and the SDK's connection helper accepts
// either form, adding the scheme when it is missing, while ListIndexes answers
// with the bare host. Matching on the raw strings would reject a caller who
// pasted the URL from the console.
func TestValidateIndexMetricAcceptsEitherHostForm(t *testing.T) {
	t.Parallel()

	indexes := []*pinecone.Index{
		{Name: "docs", Host: "docs-abc.svc.us-east1-aws.pinecone.io", Metric: pinecone.Euclidean},
	}

	for _, host := range []string{
		"docs-abc.svc.us-east1-aws.pinecone.io",
		"https://docs-abc.svc.us-east1-aws.pinecone.io",
	} {
		if err := validateIndexMetric(indexes, host, DistanceEuclidean); err != nil {
			t.Fatalf("validateIndexMetric(%q) = %v, want nil", host, err)
		}
	}
}

// The metric vocabulary is a copy of the SDK's, so the two have to keep
// spelling the same thing; a rename on either side would otherwise turn every
// comparison above into a silent mismatch.
func TestDistanceMetricMatchesTheSDKVocabulary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		metric DistanceMetric
		sdk    pinecone.IndexMetric
	}{
		{metric: DistanceCosine, sdk: pinecone.Cosine},
		{metric: DistanceDot, sdk: pinecone.Dotproduct},
		{metric: DistanceEuclidean, sdk: pinecone.Euclidean},
	}

	for _, test := range tests {
		if string(test.metric) != string(test.sdk) {
			t.Errorf("DistanceMetric %q does not match SDK metric %q", test.metric, test.sdk)
		}
		if !test.metric.Valid() {
			t.Errorf("DistanceMetric %q is not accepted by Valid()", test.metric)
		}
	}
}

// Pinecone documents the restricted-key case as ordinary: a key with custom
// permissions cannot read the control plane, which is why a caller "must target
// your index by host when performing data operations" -- the shape this store's
// IndexHost already has. Demanding control-plane read as the price of
// construction would break that documented pattern, so a denial leaves the
// metric unverified instead of failing.
func TestVerifyIndexMetricToleratesAnAuthorizationDenial(t *testing.T) {
	t.Parallel()

	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		if !isAuthorizationDenied(&pinecone.PineconeError{Code: code, Msg: errors.New("denied")}) {
			t.Errorf("isAuthorizationDenied(%d) = false, want true", code)
		}
	}
}

// Any other failure is reported. A store that cannot tell why it failed to look
// has established nothing, and saying so beats reporting agreement it never
// saw.
func TestVerifyIndexMetricReportsEveryOtherFailure(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		&pinecone.PineconeError{Code: http.StatusInternalServerError, Msg: errors.New("boom")},
		&pinecone.PineconeError{Code: http.StatusNotFound, Msg: errors.New("gone")},
		errors.New("dial tcp: connection refused"),
	} {
		if isAuthorizationDenied(err) {
			t.Errorf("isAuthorizationDenied(%v) = true, want false", err)
		}
	}
}
