package llm

import "testing"

func TestRealtimeOnlyModelsAreNotOffered(t *testing.T) {
	for id, want := range map[string]bool{
		"gemini-3.8-live": true, "gemini-2.5-flash-preview-tts": true, "gemini-2.5-flash-native-audio-dialog": true,
		"gpt-4o-realtime-preview": true, "gemini-3.8-flash": false, "gemini-3.8-pro": false, "deliver-7b": false, "olive-chat": false,
	} {
		if got := realtimeOnly(id); got != want {
			t.Errorf("realtimeOnly(%q) = %v, want %v", id, got, want)
		}
	}
}
