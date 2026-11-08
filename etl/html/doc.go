// Package html reads HTML payloads with github.com/PuerkitoBio/goquery.
//
// By default the reader emits one document with static body text and the
// page title, description, and canonical URL as metadata. Hidden subtrees and
// non-content elements are omitted; CSS and JavaScript are not evaluated. With
// [ReaderConfig.Selector] it emits one document per element matched by the CSS
// selector, subject to the same exclusions, including hidden ancestors.
package html
