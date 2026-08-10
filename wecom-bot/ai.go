package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func (b *bot) answerWeLink(ctx context.Context, sessionKey string, history []llmMessage) (string, error) {
	messages := append([]llmMessage(nil), history...)
	if len(messages) == 0 {
		return "", fmt.Errorf("missing user question")
	}
	payload, err := json.Marshal(map[string]any{
		"username":         "__cross_contact__",
		"is_group":         true,
		"messages":         messages,
		"prompt_template":  "cross_qa_answer",
		"query":            messages[len(messages)-1].Content,
		"conversation_key": sessionKey,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.WeLinkBaseURL+"/api/ai/analyze", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.cfg.WeLinkToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.WeLinkToken)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("WeLink /api/ai/analyze returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var answer strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var chunk struct {
			Delta string `json:"delta"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &chunk); err != nil {
			continue
		}
		if chunk.Error != "" {
			return "", fmt.Errorf("%s", chunk.Error)
		}
		answer.WriteString(chunk.Delta)
	}
	return answer.String(), scanner.Err()
}
