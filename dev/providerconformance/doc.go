// Package providerconformance checks the cross-provider consistency that no
// single provider module can check for itself: that every provider constructs
// the same way, exposes the same shape, and classifies failures with the same
// vocabulary. It is development tooling; product modules never depend on it.
//
// Provider modules run core/modeltest against themselves, which proves that one
// provider obeys the protocol. Agreement across the family needs every provider
// visible at once, which a provider module must not have because providers
// never import siblings.
//
// Model constructor checks discover providers from the source tree, and the web
// suites fail when a provider asserting a web contract is missing from them. A
// provider that fails a check is inconsistent with the family: fix the provider
// before relaxing the check.
package providerconformance
