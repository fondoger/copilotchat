package copilot

// Pointers distinguish an absent usage field from a reported zero. Anthropic
// sends input counts in message_start and cumulative output counts in deltas.
type wireUsage struct {
	PromptTokens     *int `json:"prompt_tokens"`
	CompletionTokens *int `json:"completion_tokens"`
	InputTokens      *int `json:"input_tokens"`
	OutputTokens     *int `json:"output_tokens"`
	CacheRead        *int `json:"cache_read_input_tokens"`
	CacheCreation    *int `json:"cache_creation_input_tokens"`
	PromptDetails    struct {
		Cached *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	InputDetails struct {
		Cached *int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (p *streamParser) usage(update *wireUsage, endpoint string) {
	if update == nil {
		return
	}
	old := &p.usageState
	assign := func(target **int, value *int) {
		if value != nil && *value >= 0 {
			*target = value
		}
	}
	assign(&old.PromptTokens, update.PromptTokens)
	assign(&old.CompletionTokens, update.CompletionTokens)
	assign(&old.InputTokens, update.InputTokens)
	assign(&old.OutputTokens, update.OutputTokens)
	assign(&old.CacheRead, update.CacheRead)
	assign(&old.CacheCreation, update.CacheCreation)
	assign(&old.PromptDetails.Cached, update.PromptDetails.Cached)
	assign(&old.InputDetails.Cached, update.InputDetails.Cached)
	value := func(number *int) int {
		if number != nil {
			return *number
		}
		return 0
	}
	usage := &p.completion.Usage
	switch endpoint {
	case "/chat/completions":
		usage.InputTokens, usage.OutputTokens = value(old.PromptTokens), value(old.CompletionTokens)
		usage.CachedInputTokens = value(old.PromptDetails.Cached)
		usage.InputAvailable, usage.OutputAvailable = old.PromptTokens != nil, old.CompletionTokens != nil
		usage.CachedInputAvailable = old.PromptDetails.Cached != nil
	case "/v1/messages":
		usage.InputTokens = value(old.InputTokens) + value(old.CacheRead) + value(old.CacheCreation)
		usage.OutputTokens, usage.CachedInputTokens = value(old.OutputTokens), value(old.CacheRead)
		usage.InputAvailable, usage.OutputAvailable = old.InputTokens != nil, old.OutputTokens != nil
		usage.CachedInputAvailable = old.CacheRead != nil
	default:
		usage.InputTokens, usage.OutputTokens = value(old.InputTokens), value(old.OutputTokens)
		usage.CachedInputTokens = value(old.InputDetails.Cached)
		usage.InputAvailable, usage.OutputAvailable = old.InputTokens != nil, old.OutputTokens != nil
		usage.CachedInputAvailable = old.InputDetails.Cached != nil
	}
	usage.Available = usage.InputAvailable || usage.OutputAvailable || usage.CachedInputAvailable
}
