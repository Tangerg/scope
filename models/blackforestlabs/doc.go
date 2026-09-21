// Package blackforestlabs wraps Black Forest Labs' FLUX image generation API.
//
// [NewImageModel] targets the official asynchronous /v1/{model} endpoints,
// follows each response's polling_url, and downloads the short-lived signed
// output before returning it.
//
// Set the output encoding through image.Options.OutputFormat. FLUX-specific
// knobs (steps, guidance, raw, safety_tolerance, prompt_upsampling, and
// image_prompt for editing) use Options.Extensions under ImageRequestExtensionKey.
//
// See https://docs.bfl.ai/ for the full reference.
package blackforestlabs
