package repository

import "testing"

func TestSupportedMIMETypes(t *testing.T) {
	supported := map[string]AssetType{
		"image/jpeg":      ImageAsset,
		"image/png":       ImageAsset,
		"image/webp":      ImageAsset,
		"video/mp4":       VideoAsset,
		"video/quicktime": VideoAsset,
	}

	for mime, want := range supported {
		if !IsSupportedMIMEType(mime) {
			t.Errorf("IsSupportedMIMEType(%q) = false, want true", mime)
		}
		if got := SupportedMIMETypes[mime]; got != want {
			t.Errorf("SupportedMIMETypes[%q] = %v, want %v", mime, got, want)
		}
	}

	// The gate must reject types the pipeline cannot process, even ones the
	// broad classifier would still bucket (e.g. gif, pdf).
	for _, mime := range []string{"image/gif", "application/pdf", "audio/mpeg", "text/plain", ""} {
		if IsSupportedMIMEType(mime) {
			t.Errorf("IsSupportedMIMEType(%q) = true, want false", mime)
		}
	}
}
