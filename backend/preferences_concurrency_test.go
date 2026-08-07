package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestPreferencesConcurrentWrites 验证 H1 修复：
//   - 并发的 updatePreferences / savePreferences 不会写坏文件（始终是合法 JSON）
//   - 用 updatePreferences 做"读-改-写"不会丢更新（last-write-wins 竞态已被锁消除）
//
// 配合 `go test -race` 运行可同时检测数据竞争。
func TestPreferencesConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "preferences.json")
	t.Setenv("PREFERENCES_PATH", path)

	// 初始写一份基线
	if err := savePreferences(defaultPreferences()); err != nil {
		t.Fatalf("初始化 preferences 失败：%v", err)
	}

	const writers = 16
	const itersPerWriter = 30

	var wg sync.WaitGroup

	// 一组 writer 各自反复 updatePreferences，往 BlockedUsers 里追加自己的唯一标记。
	// 若读-改-写有丢更新，最终条数会少于 writers*itersPerWriter。
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < itersPerWriter; i++ {
				marker := mkMarker(w, i)
				if _, err := updatePreferences(func(p *Preferences) {
					p.BlockedUsers = append(p.BlockedUsers, marker)
				}); err != nil {
					t.Errorf("updatePreferences 失败：%v", err)
					return
				}
			}
		}(w)
	}

	// 另一组并发读，确保读到的永远是合法 JSON（非原子写会读到截断/空文件）。
	var stop sync.WaitGroup
	done := make(chan struct{})
	for r := 0; r < 4; r++ {
		stop.Add(1)
		go func() {
			defer stop.Done()
			for {
				select {
				case <-done:
					return
				default:
					data, err := os.ReadFile(path)
					if err != nil {
						continue // rename 间隙偶发 ENOENT 可接受
					}
					var p Preferences
					if err := json.Unmarshal(data, &p); err != nil {
						t.Errorf("并发读到损坏的 preferences.json：%v", err)
						return
					}
				}
			}
		}()
	}

	wg.Wait()
	close(done)
	stop.Wait()

	// 校验无丢更新：所有 marker 都在
	final := loadPreferences()
	got := make(map[string]bool, len(final.BlockedUsers))
	for _, m := range final.BlockedUsers {
		got[m] = true
	}
	missing := 0
	for w := 0; w < writers; w++ {
		for i := 0; i < itersPerWriter; i++ {
			if !got[mkMarker(w, i)] {
				missing++
			}
		}
	}
	if missing > 0 {
		t.Errorf("检测到丢更新：%d/%d 个标记缺失（读-改-写竞态未被消除）", missing, writers*itersPerWriter)
	}
}

func mkMarker(w, i int) string {
	return "u-" + itoa(w) + "-" + itoa(i)
}

func TestMigrateLegacyAIConfigsToProfiles(t *testing.T) {
	raw := []byte(`{
  "schema_version": 2,
  "llm_provider": "custom", "llm_api_key": "llm-key", "llm_base_url": "https://llm.example/v1", "llm_model": "my-model",
  "embedding_provider": "custom", "embedding_api_key": "embedding-key", "embedding_base_url": "https://embedding.example/v1", "embedding_model": "embed-model", "embedding_dims": 768,
  "mem_llm_api_key": "mem-key", "mem_llm_base_url": "https://memory.example/v1", "mem_llm_model": "mem-model",
  "rerank_provider": "custom", "rerank_api_key": "rerank-key", "rerank_base_url": "https://rerank.example/v1", "rerank_model": "rerank-model"
}`)
	p, err := decodePreferences(raw)
	if err != nil {
		t.Fatalf("解析旧配置失败：%v", err)
	}
	p = migratePreferences(p)
	if p.DefaultLLMProfileID != "llm-default" || len(p.LLMProfiles) != 1 || p.LLMProfiles[0].APIKey != "llm-key" {
		t.Fatalf("LLM 迁移错误：%+v", p)
	}
	if len(p.EmbeddingProfiles) != 1 || p.EmbeddingProfiles[0].APIKey != "embedding-key" {
		t.Fatalf("Embedding 迁移错误：%+v", p.EmbeddingProfiles)
	}
	if len(p.MemLLMProfiles) != 1 || p.MemLLMProfiles[0].APIKey != "mem-key" {
		t.Fatalf("记忆 LLM 迁移错误：%+v", p.MemLLMProfiles)
	}
	if len(p.RerankProfiles) != 1 || p.RerankProfiles[0].APIKey != "rerank-key" {
		t.Fatalf("Rerank 迁移错误：%+v", p.RerankProfiles)
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("序列化新配置失败：%v", err)
	}
	for _, key := range []string{
		"llm_provider", "llm_api_key", "llm_base_url", "llm_model",
		"embedding_provider", "embedding_api_key", "embedding_base_url", "embedding_model", "embedding_dims",
		"mem_llm_api_key", "mem_llm_base_url", "mem_llm_model",
		"rerank_provider", "rerank_api_key", "rerank_base_url", "rerank_model",
	} {
		if string(encoded) == "" || containsJSONKey(encoded, key) {
			t.Errorf("迁移后仍持久化顶层字段 %q：%s", key, encoded)
		}
	}
}

func containsJSONKey(data []byte, key string) bool {
	var obj map[string]json.RawMessage
	return json.Unmarshal(data, &obj) == nil && obj[key] != nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	pos := len(b)
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
