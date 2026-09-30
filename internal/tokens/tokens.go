// Package tokens counts text locally, without contacting a model provider.
package tokens

import "github.com/tiktoken-go/tokenizer"

type Counter struct {
	tokenizer.Codec
	Estimated bool
}

// Unknown model tokenizers, including Claude, use an explicitly labeled
// o200k_base estimate. Provider usage remains authoritative for full requests.
func New(model string) (Counter, error) {
	codec, err := tokenizer.ForModel(tokenizer.Model(model))
	if err == nil {
		return Counter{Codec: codec}, nil
	}
	codec, err = tokenizer.Get(tokenizer.O200kBase)
	return Counter{Codec: codec, Estimated: true}, err
}
