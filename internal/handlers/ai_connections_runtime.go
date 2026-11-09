package handlers

import (
	"context"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/aiproviders"
)

func (a *App) generateConnectedAI(ctx context.Context, settings *models.ChatbotSettings, binding models.ChatbotAIBinding, session *models.ChatbotSession, prompt string) (string, error) {
	maxTokens := settings.AI.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 500
	}
	maxTokens = min(maxTokens, aiproviders.MaxTokens)
	start := time.Now()
	reply, err := a.AIConnections.Run(ctx, binding, maxTokens, func() []aiproviders.Message {
		var data string
		if binding.IncludeContext {
			data = a.buildAIContext(settings.OrganizationID, session, prompt)
		}
		limit := settings.AI.HistoryLimit
		if limit <= 0 {
			limit = 4
		}
		return a.chatCompletionMessages(settings, session, prompt, data, binding.IncludeHistory, min(limit, 32))
	})
	fields := []any{"connection_id", binding.ConnectionID, "latency_ms", time.Since(start).Milliseconds()}
	if err != nil {
		a.Log.Warn("Custom chatbot AI failed", append(fields, "status", aiErrorCategory(err))...)
		return "", aiproviders.ErrUnavailable
	}
	if reply.Usage != nil {
		fields = append(fields, "input_tokens", reply.Usage.Input, "output_tokens", reply.Usage.Output)
	}
	a.Log.Info("Custom chatbot AI completed", append(fields, "status", "ok")...)
	return reply.Text, nil
}
