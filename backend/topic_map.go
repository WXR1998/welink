package main

// topic_map.go — 「话题图谱」 Lab（AI 聚类）
//
// 回答一个现有 Lab 都没回答的问题：「我这一年/这段时间，到底都在聊什么？」
//
// 两步走：
//   1. 本地纯词频（service.GetTopicCorpus）：扫 Top N 私聊，抽出一批跨联系人高频词，
//      每个词带「主要和谁聊」的归属。零网络。
//   2. LLM 聚类：把这批词喂给用户自己配置的 LLM，让它聚成有名字 + emoji 的「主题」，
//      给出占比、归属对象、代表词。主题是从你真实语料里"涌现"的，
//      所以可能出现「考研冲刺 / 装修选材 / 猫猫日常」这种贴身主题，而非预置词典框死的几类。
//
// 受全局时间范围影响（GetTopicCorpus 走 timeWhere）：前端切「今年 / 全部」即可换口径。
//
// 缓存：本地 corpus 与 LLM 结果都缓存 30 分钟（按 profile 区分），?refresh=1 强制重算。
//
// API:
//   POST /api/labs/topic-map   body: {profile_id?: string}

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"welink/backend/service"
)

const (
	tmTopContacts  = 60
	tmWordsPerUser = 40
	tmMaxWords     = 150
	tmCacheTTL     = 30 * time.Minute
)

// TMTheme LLM 聚出的一个主题
type TMTheme struct {
	Emoji       string   `json:"emoji"`        // 一个代表 emoji
	Name        string   `json:"name"`         // 主题名（4-10 字）
	Percent     int      `json:"percent"`      // 占比 0-100（所有主题合计≈100）
	Keywords    []string `json:"keywords"`     // 代表词 3-6 个
	TopContacts []string `json:"top_contacts"` // 最常聊这个主题的人 0-3 位
	Blurb       string   `json:"blurb"`        // 一句话点评（15-30 字）
}

// TMResponse 接口响应
type TMResponse struct {
	Themes          []TMTheme `json:"themes"`
	ScannedContacts int       `json:"scanned_contacts"`
	TotalContacts   int       `json:"total_contacts"`
	WordsAnalyzed   int       `json:"words_analyzed"`
	GeneratedAt     int64     `json:"generated_at"`
}

type tmCacheEntry struct {
	val *TMResponse
	at  time.Time
}

var (
	tmCacheMu sync.Mutex
	tmCache   = map[string]tmCacheEntry{} // key = profileID（""=默认）
)

func registerTopicMapRoutes(prot *gin.RouterGroup, getSvc func() *service.ContactService) {
	prot.POST("/labs/topic-map", topicMapHandler(getSvc))
}

func topicMapHandler(getSvc func() *service.ContactService) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc := getSvc()
		if svc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "服务未初始化"})
			return
		}
		var body struct {
			ProfileID string `json:"profile_id"`
			Refresh   bool   `json:"refresh"`
		}
		_ = c.ShouldBindJSON(&body) // body 可选

		key := body.ProfileID
		if !body.Refresh {
			tmCacheMu.Lock()
			if e, ok := tmCache[key]; ok && time.Since(e.at) < tmCacheTTL {
				val := *e.val
				tmCacheMu.Unlock()
				c.JSON(http.StatusOK, val)
				return
			}
			tmCacheMu.Unlock()
		}

		// 1. 本地抽词
		corpus := svc.GetTopicCorpus(tmTopContacts, tmWordsPerUser, tmMaxWords)
		if corpus == nil || len(corpus.Words) < 15 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "可分析的高频词太少 —— 需要更多聊天记录，或把时间范围放宽到「全部」",
			})
			return
		}

		// 2. LLM 聚类
		prefs := loadPreferences()
		profPrefs := prefs
		if body.ProfileID != "" {
			profPrefs.DefaultLLMProfileID = body.ProfileID
		}

		// 联系人名匿名化后再送 LLM（M7），拿到结果用 reverse 还原
		alias, reverse := tmContactAlias(corpus)

		raw, err := CompleteLLM([]LLMMessage{
			{Role: "system", Content: tmSystemPrompt},
			{Role: "user", Content: tmBuildUserPrompt(corpus, alias)},
		}, profPrefs)
		if err != nil {
			// 不回显原始 err（含 provider 响应体片段），仅服务端日志（M6）
			log.Printf("[topic-map] LLM 调用失败：%v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "LLM 调用失败，请稍后重试或检查 AI 配置"})
			return
		}

		raw = stripCodeFence(strings.TrimSpace(raw))
		var parsed struct {
			Themes []TMTheme `json:"themes"`
		}
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			// 不把 raw（含化名/高频词）和原始 err 回前端，仅记服务端日志（M6）
			log.Printf("[topic-map] LLM 返回 JSON 解析失败：%v；raw=%q", err, raw)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "AI 返回格式异常，请重试"})
			return
		}
		if len(parsed.Themes) == 0 {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "AI 没聚出任何主题，请重试"})
			return
		}

		resp := &TMResponse{
			Themes:          tmNormalize(parsed.Themes, reverse),
			ScannedContacts: corpus.ScannedContacts,
			TotalContacts:   corpus.TotalContacts,
			WordsAnalyzed:   len(corpus.Words),
			GeneratedAt:     time.Now().Unix(),
		}

		tmCacheMu.Lock()
		tmCache[key] = tmCacheEntry{val: resp, at: time.Now()}
		tmCacheMu.Unlock()

		c.JSON(http.StatusOK, resp)
	}
}

const tmSystemPrompt = `你是一个聊天数据分析助手。用户会给你一批从 TA 微信聊天记录里抽出的高频词，每个词附带词频和"主要和谁聊这个词"的归属信息。

你的任务：把这些词聚类成 5-8 个有意义的「话题」，输出 JSON。

要求：
1. 主题名要具体、贴近真实生活（如"考研冲刺""装修选材""猫猫日常""相亲吐槽"），不要笼统的"日常""其他"。能合并的近义词合并到同一主题。
2. 每个主题给一个最贴切的 emoji。
3. percent 是该主题在所有话题里的大致占比（整数，所有主题合计接近 100）。按主题包含词的总词频估算。
4. keywords 选 3-6 个最能代表该主题的原词（必须来自输入词表）。
5. top_contacts 填这个主题最常聊的 1-3 个人（从输入里词的归属信息汇总，取出现最多的人名）。聊天对象不明确就给空数组。
6. blurb 一句 15-30 字的点评，口语、有梗、像朋友吐槽，不要客套。
7. 噪声词（无法归入任何有意义主题的零散词）直接丢弃，不要硬凑主题。

严格只输出 JSON，不要任何解释文字、不要 markdown 代码块：
{"themes":[{"emoji":"📚","name":"考研冲刺","percent":22,"keywords":["真题","背单词","报名"],"top_contacts":["小王","老张"],"blurb":"你和小王今年的命运共同体就是这场考试"}]}`

// tmContactAlias 为 corpus 里出现的真实联系人名建立"真名 → 化名（联系人1/2…）"映射。
// 隐私（M7）：联系人显示名是社交关系元数据，不应原样发往第三方 LLM。
// 发送前替换成化名，拿到结果后再用 reverse 还原成真名给前端展示。
// 返回 alias(真名→化名) 与 reverse(化名→真名) 两张表；按"首次出现顺序"编号，保证确定性。
func tmContactAlias(corpus *service.TopicCorpus) (alias, reverse map[string]string) {
	alias = map[string]string{}
	reverse = map[string]string{}
	n := 0
	for _, w := range corpus.Words {
		for _, name := range w.TopContacts {
			if name == "" {
				continue
			}
			if _, ok := alias[name]; ok {
				continue
			}
			n++
			a := fmt.Sprintf("联系人%d", n)
			alias[name] = a
			reverse[a] = name
		}
	}
	return alias, reverse
}

// tmBuildUserPrompt 把 corpus 拼成给 LLM 的输入文本；联系人名以 alias 替换为化名。
func tmBuildUserPrompt(corpus *service.TopicCorpus, alias map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "以下是从我的微信聊天里抽出的 %d 个高频词（已扫描 %d 个私聊），格式：词｜词频｜主要和谁聊。\n请据此聚类成话题。\n\n",
		len(corpus.Words), corpus.ScannedContacts)
	for _, w := range corpus.Words {
		who := "—"
		if len(w.TopContacts) > 0 {
			aliased := make([]string, 0, len(w.TopContacts))
			for _, name := range w.TopContacts {
				if a, ok := alias[name]; ok {
					aliased = append(aliased, a)
				}
			}
			if len(aliased) > 0 {
				who = strings.Join(aliased, "、")
			}
		}
		fmt.Fprintf(&b, "%s｜%d｜%s\n", w.Word, w.Count, who)
	}
	return b.String()
}

// tmNormalize 修正 LLM 输出：裁剪字段长度、夹紧 percent、按占比降序，
// 并用 reverse（化名→真名）把 top_contacts 还原成真实联系人名给前端展示（M7）。
func tmNormalize(themes []TMTheme, reverse map[string]string) []TMTheme {
	out := make([]TMTheme, 0, len(themes))
	for _, t := range themes {
		if strings.TrimSpace(t.Name) == "" {
			continue
		}
		if t.Percent < 0 {
			t.Percent = 0
		}
		if t.Percent > 100 {
			t.Percent = 100
		}
		if len(t.Keywords) > 6 {
			t.Keywords = t.Keywords[:6]
		}
		if len(t.TopContacts) > 3 {
			t.TopContacts = t.TopContacts[:3]
		}
		// 化名 → 真名；LLM 万一返回未知名（幻觉）则原样保留
		for i, name := range t.TopContacts {
			if real, ok := reverse[name]; ok {
				t.TopContacts[i] = real
			}
		}
		if t.Emoji == "" {
			t.Emoji = "💬"
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Percent > out[j].Percent })
	return out
}
