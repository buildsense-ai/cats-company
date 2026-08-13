package server

import (
	"strings"
	"testing"
)

func TestInferChannelMediaExtForAudioWithoutFilename(t *testing.T) {
	testCases := []struct {
		contentType string
		want        string
	}{
		{contentType: "audio/ogg; codecs=opus", want: ".ogg"},
		{contentType: "application/ogg", want: ".ogg"},
		{contentType: "audio/mpeg", want: ".mp3"},
		{contentType: "audio/mp3", want: ".mp3"},
		{contentType: "audio/wav", want: ".wav"},
		{contentType: "audio/x-wav", want: ".wav"},
	}

	for _, tc := range testCases {
		t.Run(tc.contentType, func(t *testing.T) {
			if got := inferChannelMediaExt("file", tc.contentType); got != tc.want {
				t.Fatalf("inferChannelMediaExt(%q) = %q, want %q", tc.contentType, got, tc.want)
			}
		})
	}
}

func TestSaveChannelMediaUploadPreservesOggVideoMetadata(t *testing.T) {
	t.Chdir(t.TempDir())

	file, err := saveChannelMediaUploadFromReader(
		"file",
		"clip.ogg",
		"video/ogg",
		strings.NewReader("OggS video stream"),
	)
	if err != nil {
		t.Fatalf("save channel media: %v", err)
	}
	if !strings.HasSuffix(file.FileKey, ".ogv") {
		t.Fatalf("file key = %q, want .ogv suffix", file.FileKey)
	}
	if !strings.HasSuffix(file.URL, ".ogv") {
		t.Fatalf("url = %q, want .ogv suffix", file.URL)
	}
	if file.MimeType != "video/ogg" {
		t.Fatalf("mime type = %q, want video/ogg", file.MimeType)
	}
}
