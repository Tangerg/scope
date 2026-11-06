package assemblyai

import "errors"

// The resolved selection is the only source of language fields in an outgoing
// transcript. Defaults and per-call controls must agree before audio is uploaded.
type languageSelection struct {
	code      string
	codes     []string
	detection *bool
}

func newLanguageSelection(code string, codes []string, detection *bool) (languageSelection, error) {
	automatic := detection != nil && *detection
	if code != "" && (codes != nil || automatic) || codes != nil && automatic {
		return languageSelection{}, errors.New("assemblyai: language_code, language_codes, and enabled language_detection are mutually exclusive")
	}
	return languageSelection{code: code, codes: codes, detection: detection}, nil
}

func (l languageSelection) apply(request *transcriptRequest) {
	request.LanguageCode = l.code
	request.LanguageCodes = l.codes
	request.LanguageDetection = l.detection
}
