package llm

import "encoding/json"

type openaiUsageUpdate struct {
	PromptTokens        *int `json:"prompt_tokens"`
	CompletionTokens    *int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	PromptCacheHitTokens json.RawMessage `json:"prompt_cache_hit_tokens"`
	Opencode             json.RawMessage `json:"opencode"`
}

// 可选扩展的无效值不影响标准计数与正文。
func openaiAliasToken(raw json.RawMessage) *int {
	var value *int
	if json.Unmarshal(raw, &value) != nil || value == nil || *value < 0 {
		return nil
	}
	return value
}

func (u openaiUsageUpdate) cacheRead() *int {
	if u.PromptTokensDetails.CachedTokens != nil {
		return u.PromptTokensDetails.CachedTokens
	}
	if value := openaiAliasToken(u.PromptCacheHitTokens); value != nil {
		return value
	}
	var extension struct {
		Tokens struct {
			CacheRead json.RawMessage `json:"cache_read"`
		} `json:"tokens"`
	}
	if json.Unmarshal(u.Opencode, &extension) != nil {
		return nil
	}
	return openaiAliasToken(extension.Tokens.CacheRead)
}

// 网关可能分字段发送累计快照；显式零与下降修正同样覆盖旧值。
func (u openaiUsageUpdate) merge(previous Usage) Usage {
	if u.PromptTokens != nil {
		previous.InputTokens = *u.PromptTokens
	}
	if u.CompletionTokens != nil {
		previous.OutputTokens = *u.CompletionTokens
	}
	if cached := u.cacheRead(); cached != nil {
		previous.CacheReadTokens = *cached
	}
	return previous
}
