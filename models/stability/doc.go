// Package stability wraps Stability AI's image generation REST API.
//
// [NewImageModel] targets the v2beta /stable-image/generate endpoints
// (ultra, core, sd3, etc.). Per-model knobs (aspect_ratio, negative_prompt,
// seed, style_preset, output_format) ride through the typed
// [image.Options] fields plus extension-threaded params.
//
// Stability's edit / upscale / control / video surfaces ship at sibling
// paths under /v2beta but require a different request shape; they're
// not modeled here.
//
// Filtered generations. The adapter requests the JSON envelope so it can read
// finish_reason, because a generation the safety classifier stops still answers
// 200 with base64 bytes and those bytes are the classifier's stand-in, not the
// requested image. CONTENT_FILTERED is returned as an error, as is any
// finish_reason this adapter cannot classify.
//
// See https://platform.stability.ai/docs/api-reference for the full
// reference.
package stability
