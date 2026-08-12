package server

import "testing"

func TestChannelInboundMediaClassifiesAudioForPlayback(t *testing.T) {
	file := uploadPayload{
		FileKey:  "20260812_0123456789abcdef0123456789abcdef.ogg",
		URL:      "/uploads/files/20260812_0123456789abcdef0123456789abcdef.ogg",
		Name:     "voice.ogg",
		Size:     42,
		Type:     "file",
		MimeType: "audio/ogg; codecs=opus",
	}

	blocks := channelInboundContentBlocks("", []uploadPayload{file})
	if len(blocks) != 1 || blocks[0].Type != "audio" {
		t.Fatalf("blocks = %#v, want one audio block", blocks)
	}
	if got := channelInboundMessageType(file); got != "voice" {
		t.Fatalf("channelInboundMessageType = %q, want voice", got)
	}
	if got := channelInboundContentBlockType(uploadPayload{Type: "file", MimeType: "video/ogg"}); got != "file" {
		t.Fatalf("video/ogg block type = %q, want file", got)
	}
}
