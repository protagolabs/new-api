package controller

import "strings"

// nonChatTestModelPrefixes lists model families that cannot answer a
// chat/completions request at all: video and image generation models sit behind
// their own upstream verbs (predictLongRunning, predict, /v1/video/generations)
// and reject generateContent with a 404 that looks like a broken channel.
//
// These are prefixes of the model name rather than channel types on purpose.
// Channel types that serve *only* async tasks are already excluded wholesale in
// testChannel; this list exists for the mixed ones -- notably Gemini (type 24),
// where the same channel carries chat models and Veo/Imagen side by side.
var nonChatTestModelPrefixes = []string{
	"veo-",    // Google video, predictLongRunning
	"imagen-", // Google image, predict
}

// isNonChatTestModel reports whether a channel test against this model would be
// meaningless regardless of whether the channel itself is healthy.
func isNonChatTestModel(modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if name == "" {
		return false
	}
	for _, prefix := range nonChatTestModelPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
