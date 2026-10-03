package main

import "github.com/joel299/agentic-voice-sdr/internal/telephony/baresipmedia"

func localBaresipMediaConfig() baresipmedia.Config {
	// Restore 871efdf's bounded defaults (32 RX/TX frames). The latency
	// experiment's 80ms RX budget erased speech arriving during Gemini setup.
	return baresipmedia.Config{}
}
