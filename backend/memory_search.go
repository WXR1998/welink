package main

// memory_search.go — 记忆优先的两级检索
//
// 把 mem_facts 当作语义索引层：
//   问题 → embedding → 搜索 mem_facts（Level 1：精炼事实）
//                          ↓ source_from / source_to
//                   提取源聊天记录（Level 2：vec_messages）
//                          ↓
//               facts + 源聊天记录 → 组装 context → 送 LLM
//
// 相比直接搜原始聊天记录：
//   1. mem_facts 是 LLM 已提炼、去重后的事实，搜索精度高
//   2. embedding 搜索天然处理近义词/转述
//   3. 每条 fact 的 source_from/source_to 指向 vec_messages 源消息，可精准追溯



// SourceMessage 是记忆事实对应的源聊天记录中的一条消息。
type SourceMessage struct {
	Seq      int    `json:"seq"`
	Datetime string `json:"datetime"`
	Sender   string `json:"sender"`
	Content  string `json:"content"`
}

// FactSource 是一条记忆事实及其对应的源聊天记录。
type FactSource struct {
	Fact     MemFact         `json:"fact"`     // 记忆事实（含 ContactKey, SourceFrom, SourceTo）
	Messages []SourceMessage `json:"messages"` // 源聊天记录
}

// ExtractFactSources 批量提取记忆事实对应的源聊天记录。
//
// 对每条 fact，用 source_from/source_to 从 vec_messages 提取源消息。
// 手工添加的 fact（source_from=0, source_to=0）会被跳过——这类 fact 没有
// 对应的源聊天记录区间，只是用户手写的背景知识。
//
// extractAndStoreFacts 存储时：source_from = chunk 起始下标 i，
// source_to = chunk 结束下标 end-1。而 vec_messages.seq 也是按下标存的
// （vec.go: stmt.Exec(key, i+j, ...)），所以 source_from/source_to 直接
// 对应 vec_messages.seq 区间。
func ExtractFactSources(facts []MemFact) ([]FactSource, error) {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil || len(facts) == 0 {
		return nil, nil
	}

	var out []FactSource
	for _, f := range facts {
		// 跳过手工添加的 fact（source_from=0, source_to=0）
		// extractAndStoreFacts 存的 source_from=i, source_to=end-1
		// 第一批: i=0, end=80 → source_from=0, source_to=79
		// 手工添加: source_from=0, source_to=0 → 跳过
		if f.SourceFrom == 0 && f.SourceTo == 0 {
			continue
		}
		limit := f.SourceTo - f.SourceFrom + 1
		if limit <= 0 {
			continue
		}

		rows, err := db.Query(
			`SELECT seq, datetime, sender, content FROM vec_messages
			 WHERE contact_key = ? ORDER BY seq LIMIT ? OFFSET ?`,
			f.ContactKey, limit, f.SourceFrom)
		if err != nil {
			continue
		}

		var msgs []SourceMessage
		for rows.Next() {
			var m SourceMessage
			rows.Scan(&m.Seq, &m.Datetime, &m.Sender, &m.Content)
			msgs = append(msgs, m)
		}
		rows.Close()

		if len(msgs) > 0 {
			out = append(out, FactSource{
				Fact:     f,
				Messages: msgs,
			})
		}
	}
	return out, nil
}

