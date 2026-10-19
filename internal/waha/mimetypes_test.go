package waha

import "testing"

func TestMediaExtension(t *testing.T) {
	cases := map[string]string{
		"image/jpeg":             "jpeg",
		"audio/ogg; codecs=opus": "oga",
		"Video/QuickTime":        "qt",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "docx",
		"application/x-unknown": "bin",
		"video/mp4":             "mp4",
		"application/was":       "zip",
		"IMAGE/PNG":             "png",
		"":                      "bin",
	}
	for in, want := range cases {
		if got := MediaExtension(in); got != want {
			t.Errorf("MediaExtension(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMimetypeOfExtensionRoundTrips(t *testing.T) {
	for mt, ext := range extensions {
		back := MimetypeOfExtension(ext)
		if MediaExtension(back) != ext {
			t.Errorf("%s -> %s -> %s does not come back to %s", mt, ext, back, ext)
		}
	}
	if got := MimetypeOfExtension("nope"); got != "application/octet-stream" {
		t.Errorf("unknown extension: %q", got)
	}
}
