package main

// fallback.go — 多提供商粘性回退管理器
//
// 用于 Embedding 和记忆提炼 LLM 调用：
//   - 按 Profiles 数组顺序依次尝试
//   - 某提供商连续失败 maxFailures 次 → 切换到下一个，并保持 stickyDuration
//   - sticky 期间跳过已标记为 down 的提供商
//   - 全部提供商都失败才返回错误

import (
	"sync"
	"time"
)

// fallbackState 跟踪一组提供商的连续失败计数和粘性回退状态。
type fallbackState struct {
	mu             sync.Mutex
	failCount      int           // 当前提供商连续失败次数
	activeIdx      int           // 当前活跃提供商索引
	stickyUntil    time.Time     // 粘性回退过期时间
	maxFailures    int           // 连续失败多少次后切换
	stickyDuration time.Duration // 粘性回退保持时长
}

func newFallbackState(maxFailures int, stickyDuration time.Duration) *fallbackState {
	return &fallbackState{
		maxFailures:    maxFailures,
		stickyDuration: stickyDuration,
	}
}

// getActiveIndex 返回当前应优先尝试的提供商索引。
func (f *fallbackState) getActiveIndex(numProviders int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if numProviders == 0 {
		return 0
	}
	if f.activeIdx >= numProviders {
		f.activeIdx = 0
	}
	return f.activeIdx
}

// recordFailure 记录一次失败。如果连续失败达到阈值，切换到下一个提供商
// 并设置粘性回退过期时间。返回切换后的新活跃索引。
func (f *fallbackState) recordFailure(currentIdx, numProviders int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failCount++
	if f.failCount >= f.maxFailures && numProviders > 1 {
		f.activeIdx = (currentIdx + 1) % numProviders
		f.failCount = 0
		f.stickyUntil = time.Now().Add(f.stickyDuration)
	}
	return f.activeIdx
}

// recordSuccess 记录一次成功，重置失败计数。
func (f *fallbackState) recordSuccess() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failCount = 0
}

// 全局回退状态：embedding 和记忆提炼 LLM 各一套
var embeddingFallback = newFallbackState(2, time.Hour)
var memLLMFallback = newFallbackState(2, time.Hour)
