// Package luma wraps Luma's current Agents API using the official Go SDK.
//
// [NewImageModel] targets the async /v1/generations endpoint for the uni-1
// family. It submits, polls, and downloads expiring output URLs before
// returning provider-neutral image bytes.
//
// ImageRequestOptions supplies native options under ImageRequestExtensionKey.
// SDK outbound parameter wrappers are not extension input types. Core owns
// prompt, model, and output format. Video generation is outside core/image.
//
// See https://docs.agents.lumalabs.ai/ for the official reference.
package luma
