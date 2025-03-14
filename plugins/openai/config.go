package openai

import "github.com/firebase/genkit/go/ai"

type GenerationReasoningConfig struct {
	ai.GenerationCommonConfig
	ReasoningEffort string
}
