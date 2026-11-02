package elevenlabs

type speechOutputFormat string

const (
	speechFormatALaw8000Hz         speechOutputFormat = "alaw_8000"
	speechFormatMP322050Hz32Kbps   speechOutputFormat = "mp3_22050_32"
	speechFormatMP324000Hz48Kbps   speechOutputFormat = "mp3_24000_48"
	speechFormatMP344100Hz32Kbps   speechOutputFormat = "mp3_44100_32"
	speechFormatMP344100Hz64Kbps   speechOutputFormat = "mp3_44100_64"
	speechFormatMP344100Hz96Kbps   speechOutputFormat = "mp3_44100_96"
	speechFormatMP344100Hz128Kbps  speechOutputFormat = "mp3_44100_128"
	speechFormatMP344100Hz192Kbps  speechOutputFormat = "mp3_44100_192"
	speechFormatOpus48000Hz32Kbps  speechOutputFormat = "opus_48000_32"
	speechFormatOpus48000Hz64Kbps  speechOutputFormat = "opus_48000_64"
	speechFormatOpus48000Hz96Kbps  speechOutputFormat = "opus_48000_96"
	speechFormatOpus48000Hz128Kbps speechOutputFormat = "opus_48000_128"
	speechFormatOpus48000Hz192Kbps speechOutputFormat = "opus_48000_192"
	speechFormatPCM8000Hz          speechOutputFormat = "pcm_8000"
	speechFormatPCM16000Hz         speechOutputFormat = "pcm_16000"
	speechFormatPCM22050Hz         speechOutputFormat = "pcm_22050"
	speechFormatPCM24000Hz         speechOutputFormat = "pcm_24000"
	speechFormatPCM32000Hz         speechOutputFormat = "pcm_32000"
	speechFormatPCM44100Hz         speechOutputFormat = "pcm_44100"
	speechFormatPCM48000Hz         speechOutputFormat = "pcm_48000"
	speechFormatMuLaw8000Hz        speechOutputFormat = "ulaw_8000"
	speechFormatWAV8000Hz          speechOutputFormat = "wav_8000"
	speechFormatWAV16000Hz         speechOutputFormat = "wav_16000"
	speechFormatWAV22050Hz         speechOutputFormat = "wav_22050"
	speechFormatWAV24000Hz         speechOutputFormat = "wav_24000"
	speechFormatWAV32000Hz         speechOutputFormat = "wav_32000"
	speechFormatWAV44100Hz         speechOutputFormat = "wav_44100"
	speechFormatWAV48000Hz         speechOutputFormat = "wav_48000"
)

func (s speechOutputFormat) valid() bool {
	switch s {
	case speechFormatALaw8000Hz,
		speechFormatMP322050Hz32Kbps,
		speechFormatMP324000Hz48Kbps,
		speechFormatMP344100Hz32Kbps,
		speechFormatMP344100Hz64Kbps,
		speechFormatMP344100Hz96Kbps,
		speechFormatMP344100Hz128Kbps,
		speechFormatMP344100Hz192Kbps,
		speechFormatOpus48000Hz32Kbps,
		speechFormatOpus48000Hz64Kbps,
		speechFormatOpus48000Hz96Kbps,
		speechFormatOpus48000Hz128Kbps,
		speechFormatOpus48000Hz192Kbps,
		speechFormatPCM8000Hz,
		speechFormatPCM16000Hz,
		speechFormatPCM22050Hz,
		speechFormatPCM24000Hz,
		speechFormatPCM32000Hz,
		speechFormatPCM44100Hz,
		speechFormatPCM48000Hz,
		speechFormatMuLaw8000Hz,
		speechFormatWAV8000Hz,
		speechFormatWAV16000Hz,
		speechFormatWAV22050Hz,
		speechFormatWAV24000Hz,
		speechFormatWAV32000Hz,
		speechFormatWAV44100Hz,
		speechFormatWAV48000Hz:
		return true
	default:
		return false
	}
}
