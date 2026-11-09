// Package blackforestlabs wraps Black Forest Labs' FLUX image generation API.
//
// [NewImageModel] targets the official asynchronous /v1/{model} endpoints,
// follows each response's polling_url, and downloads the short-lived signed
// output before returning it.
// Downloads admit the official BFL HTTPS namespace and its Azure delivery
// origin. ImageModelConfig.OutputOrigins authorizes additional delivery origins;
// configuring an API endpoint does not grant that endpoint delivery authority.
// Delivery requests never carry the API key, authorization headers, or cookies.
// Output metadata blackforestlabs/duration preserves the provider's native
// numeric value without assuming a duration unit.
//
// Set the output encoding through image.Options.OutputFormat. FLUX-specific
// knobs (steps, guidance, raw, safety_tolerance, prompt_upsampling, and
// image_prompt for editing) use Options.Extensions under ImageRequestExtensionKey.
// An explicit prompt_upsampling=false is preserved because endpoint defaults
// differ; omission leaves that choice to the selected model.
//
// See https://docs.bfl.ai/ for the full reference.
package blackforestlabs
