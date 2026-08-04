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
	"time"
)

// ragRequest 与后端 POST /api/ai/rag 的请求体对应。
type ragRequest struct {
	Key       string       `json:"key"`
	Messages  []llmMessage `json:"messages"`
	ProfileID string       `json:"profile_id"`
}

type llmMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ragChunk 解析后端 SSE 的 data: {...} 单帧。
type ragChunk struct {
	Delta   string `json:"delta,omitempty"`
	Done    bool   `json:"done,omitempty"`
	Error   string `json:"error,omitempty"`
	RagMeta *struct {
		Hits      int `json:"hits"`
		Retrieved int `json:"retrieved"`
	} `json:"rag_meta,omitempty"`
}

// ragOutcome 汇总一次 RAG 问答的结果。
type ragOutcome struct {
	Answer    string
	Error     string
	Hits      int
	Retrieved int
}

// askRAG 调用 WeLink /api/ai/rag，把 SSE 增量累积成完整回答返回。
func askRAG(ctx context.Context, cfg *Config, key, question, history string) (*ragOutcome, error) {
	if key == "" {
		return nil, fmt.Errorf("DEFAULT_AI_KEY 未配置，无法确定检索范围")
	}
	msgs := []llmMessage{{Role: "user", Content: question}}
	if strings.TrimSpace(history) != "" {
		msgs = []llmMessage{
			{Role: "system", Content: "以下是本会话前序上下文，可参考但以最新问题为准：\n" + history},
			{Role: "user", Content: question},
		}
	}

	payload, _ := json.Marshal(ragRequest{
		Key:       key,
		Messages:  msgs,
		ProfileID: cfg.DefaultProfileID,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.WeLinkBaseURL+"/api/ai/rag", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.WeLinkToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.WeLinkToken)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("WeLink /api/ai/rag 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	out := &ragOutcome{}
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return out, err
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if dataStr == "" {
			continue
		}
		var ch ragChunk
		if err := json.Unmarshal([]byte(dataStr), &ch); err != nil {
			continue
		}
		if ch.RagMeta != nil {
			out.Hits = ch.RagMeta.Hits
			out.Retrieved = ch.RagMeta.Retrieved
		}
		if ch.Error != "" {
			out.Error = ch.Error
			break
		}
		if ch.Delta != "" {
			out.Answer += ch.Delta
		}
		if ch.Done {
			break
		}
	}
	return out, nil
}
