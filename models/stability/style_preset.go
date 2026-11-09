package stability

import "fmt"

type stylePreset string

const (
	stylePresetEnhance          stylePreset = "enhance"
	stylePresetAnime            stylePreset = "anime"
	stylePresetPhotographic     stylePreset = "photographic"
	stylePresetDigitalArt       stylePreset = "digital-art"
	stylePresetComicBook        stylePreset = "comic-book"
	stylePresetFantasyArt       stylePreset = "fantasy-art"
	stylePresetLineArt          stylePreset = "line-art"
	stylePresetAnalogFilm       stylePreset = "analog-film"
	stylePresetNeonPunk         stylePreset = "neon-punk"
	stylePresetIsometric        stylePreset = "isometric"
	stylePresetLowPoly          stylePreset = "low-poly"
	stylePresetOrigami          stylePreset = "origami"
	stylePresetModelingCompound stylePreset = "modeling-compound"
	stylePresetCinematic        stylePreset = "cinematic"
	stylePreset3DModel          stylePreset = "3d-model"
	stylePresetPixelArt         stylePreset = "pixel-art"
	stylePresetTileTexture      stylePreset = "tile-texture"
)

func (s stylePreset) validate() error {
	switch s {
	case "", stylePresetEnhance, stylePresetAnime, stylePresetPhotographic, stylePresetDigitalArt,
		stylePresetComicBook, stylePresetFantasyArt, stylePresetLineArt, stylePresetAnalogFilm,
		stylePresetNeonPunk, stylePresetIsometric, stylePresetLowPoly, stylePresetOrigami,
		stylePresetModelingCompound, stylePresetCinematic, stylePreset3DModel, stylePresetPixelArt, stylePresetTileTexture:
		return nil
	default:
		return fmt.Errorf("stability: unsupported style_preset %q", s)
	}
}
