// Package luma wraps Luma's current Agents API using the official Go SDK.
//
// [NewImageModel] targets the async /v1/generations endpoint for the uni-1
// family. It submits, polls, and downloads expiring output URLs before
// returning provider-neutral image bytes. ImageModelConfig.OutputOrigins must
// explicitly authorize their destinations; presigned assets receive neither
// API credentials nor cookies from the borrowed HTTP client.
//
// ImageRequestOptions supplies native options under ImageRequestExtensionKey.
// SDK outbound parameter wrappers are not extension input types. Core owns
// prompt, model, and output format. Video generation is outside core/image.
//
// See https://docs.agents.lumalabs.ai/ for the official reference.
// Submission and polling metadata, including error envelopes, use Core's
// httpclient.DefaultMaxJSONResponseBytes. Asset downloads retain their separate
// MaxOutputBytes budget because binary output has a different lifetime.
package luma
