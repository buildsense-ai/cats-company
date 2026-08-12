package server

import "testing"

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
