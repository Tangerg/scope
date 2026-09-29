// Package html reads HTML payloads with github.com/PuerkitoBio/goquery.
//
// By default the reader emits one document with the visible body text and the
// page title, description, and canonical URL as metadata. With
// [ReaderConfig.Selector] it emits one document per element matched by the CSS
// selector instead.
package html
