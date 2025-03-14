package openai

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/firebase/genkit/go/ai"

	goopenai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

const (
	provider    = "openai"
	labelPrefix = "OpenAI"
	apiKeyEnv   = "OPENAI_API_KEY"
)

var state struct {
	mu      sync.Mutex
	initted bool
	client  *goopenai.Client
}

var (
	knownCaps = map[string]ai.ModelCapabilities{
		goopenai.ChatModelO3Mini:      BasicText,
		goopenai.ChatModelO1:          BasicText,
		goopenai.ChatModelGPT4o:       Multimodal,
		goopenai.ChatModelGPT4oMini:   Multimodal,
		goopenai.ChatModelGPT4Turbo:   Multimodal,
		goopenai.ChatModelGPT4:        BasicText,
		goopenai.ChatModelGPT3_5Turbo: BasicText,
	}

	modelsSupportingResponseFormats = []string{
		goopenai.ChatModelO3Mini,
		goopenai.ChatModelO1,
		goopenai.ChatModelGPT4o,
		goopenai.ChatModelGPT4oMini,
		goopenai.ChatModelGPT4Turbo,
		goopenai.ChatModelGPT3_5Turbo,
	}

	knownEmbedders = []string{
		goopenai.EmbeddingModelTextEmbedding3Small,
		goopenai.EmbeddingModelTextEmbedding3Large,
		goopenai.EmbeddingModelTextEmbeddingAda002,
	}
)

// Config is the configuration for the plugin.
type Config struct {
	// The API key to access the service.
	// If empty, the values of the environment variables OPENAI_API_KEY will be consulted.
	APIKey string
}

// Init initializes the plugin and all known models.
// After calling Init, you may call [DefineModel] to create and register any additional generative models.
func Init(ctx context.Context, cfg *Config) (err error) {
	if cfg == nil {
		cfg = &Config{}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.initted {
		panic(provider + ".Init not called")
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s.Init: %w", provider, err)
		}
	}()

	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = os.Getenv(apiKeyEnv)
		if apiKey == "" {
			return fmt.Errorf("OpenAI requires setting %s in the environment. You can get an API key at https://platform.openai.com/api-keys", apiKeyEnv)
		}
	}

	client := goopenai.NewClient(
		option.WithAPIKey(apiKey),
	)
	state.client = client
	state.initted = true
	for model, caps := range knownCaps {
		defineModel(model, caps)
	}
	for _, e := range knownEmbedders {
		defineEmbedder(e)
	}
	return nil
}

// DefineModel defines an unknown model with the given name.
// The second argument describes the capability of the model.
// Use [IsDefinedModel] to determine if a model is already defined.
// After [Init] is called, only the known models are defined.
func DefineModel(name string, caps *ai.ModelCapabilities) (ai.Model, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.initted {
		panic(provider + ".Init not called")
	}
	var mc ai.ModelCapabilities
	if caps == nil {
		var ok bool
		mc, ok = knownCaps[name]
		if !ok {
			return nil, fmt.Errorf("%s.DefineModel: called with unknown model %q and nil ModelCapabilities", provider, name)
		}
	} else {
		mc = *caps
	}
	return defineModel(name, mc), nil
}

// requires state.mu
func defineModel(name string, caps ai.ModelCapabilities) ai.Model {
	meta := &ai.ModelMetadata{
		Label:    labelPrefix + " - " + name,
		Supports: caps,
	}
	return ai.DefineModel(provider, name, meta, func(
		ctx context.Context,
		input *ai.GenerateRequest,
		cb func(context.Context, *ai.GenerateResponseChunk) error,
	) (*ai.GenerateResponse, error) {
		return generate(ctx, state.client, name, input, cb)
	})
}

// IsDefinedModel reports whether the named [Model] is defined by this plugin.
func IsDefinedModel(name string) bool {
	return ai.IsDefinedModel(provider, name)
}

// DefineEmbedder defines an embedder with a given name.
func DefineEmbedder(name string) ai.Embedder {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.initted {
		panic(provider + ".Init not called")
	}
	return defineEmbedder(name)
}

// IsDefinedEmbedder reports whether the named [Embedder] is defined by this plugin.
func IsDefinedEmbedder(name string) bool {
	return ai.IsDefinedEmbedder(provider, name)
}

// requires state.mu
func defineEmbedder(name string) ai.Embedder {
	return ai.DefineEmbedder(provider, name, func(ctx context.Context, input *ai.EmbedRequest) (*ai.EmbedResponse, error) {
		var data goopenai.EmbeddingNewParamsInputArrayOfStrings
		for _, doc := range input.Documents {
			for _, p := range doc.Content {
				data = append(data, p.Text)
			}
		}

		params := goopenai.EmbeddingNewParams{
			Input:          goopenai.F[goopenai.EmbeddingNewParamsInputUnion](data),
			Model:          goopenai.F(name),
			EncodingFormat: goopenai.F(goopenai.EmbeddingNewParamsEncodingFormatFloat),
		}

		embRes, err := state.client.Embeddings.New(ctx, params)
		if err != nil {
			return nil, err
		}

		var res ai.EmbedResponse
		for _, emb := range embRes.Data {
			embedding := make([]float32, len(emb.Embedding))
			for i, val := range emb.Embedding {
				embedding[i] = float32(val)
			}
			res.Embeddings = append(res.Embeddings, &ai.DocumentEmbedding{Embedding: embedding})
		}
		return &res, nil
	})
}

// Model returns the [ai.Model] with the given name.
// It returns nil if the model was not defined.
func Model(name string) ai.Model {
	return ai.LookupModel(provider, name)
}

// Embedder returns the [ai.Embedder] with the given name.
// It returns nil if the embedder was not defined.
func Embedder(name string) ai.Embedder {
	return ai.LookupEmbedder(provider, name)
}

func generate(
	ctx context.Context,
	client *goopenai.Client,
	model string,
	input *ai.GenerateRequest,
	cb func(context.Context, *ai.GenerateResponseChunk) error, // TODO: implement streaming
) (*ai.GenerateResponse, error) {
	req, err := convertRequest(model, input)
	if err != nil {
		return nil, err
	}

	res, err := client.Chat.Completions.New(ctx, req)
	if err != nil {
		return nil, err
	}

	jsonMode := false
	if input.Output != nil &&
		input.Output.Format == ai.OutputFormatJSON {
		jsonMode = true
	}

	r := translateResponse(res, jsonMode)
	r.Request = input
	return r, nil
}
