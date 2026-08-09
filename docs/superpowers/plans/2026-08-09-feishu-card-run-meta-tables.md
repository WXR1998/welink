# Feishu Card Run Metadata Tables Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render the completed Feishu AI-answer card's model, decomposition, and query-expansion metadata as compact, escaped Markdown tables.

**Architecture:** Keep `contextMetaLine` as the composition point and replace only `formatAnswerRunMeta`'s text construction in `feishu-bot/bot.go`. Add a small Markdown table-cell escaping helper adjacent to that formatter. The existing card JSON renderer continues to receive one Markdown body, so streaming, retrieval, and final-answer rendering remain unchanged.

**Tech Stack:** Go 1.24, Feishu card JSON 2.0 Markdown component, Go standard-library tests.

---

### Task 1: Define the formatter contract with failing tests

**Files:**
- Modify: `feishu-bot/ai_test.go:188-214`
- Modify: `feishu-bot/bot.go:905-938`

- [ ] **Step 1: Replace `TestFormatAnswerRunMeta` assertions with explicit table expectations**

```go
for _, want := range []string{
    "> **模型**",
    "| 步骤 | 模型 |",
    "| 问题分解 | `glm-5.2` |",
    "| 查询扩展 | `gpt-5.6-terra` |",
    "| 最终回答 | `gpt-5.6-terra` |",
    "> **问题分解**",
    "| 维度 | 结果 |",
    "| 实体 | 张三 |",
    "| 概念 | 旅行 |",
    "> **查询扩展**",
    "| 序号 | 查询 |",
    "| 1 | 张三旅行计划 |",
    "| 2 | 张三旅行时间 |",
} {
    if !strings.Contains(got, want) {
        t.Fatalf("meta missing %q: %s", want, got)
    }
}
```

- [ ] **Step 2: Add a test for absent models and Markdown-table escaping**

```go
func TestFormatAnswerRunMetaEscapesTableCells(t *testing.T) {
    got := formatAnswerRunMeta(answerRunMeta{
        Models: qaStepModels{FinalAnswer: "gpt-5.6"},
        ExpandedQueries: []string{"张三 | 旅行\n时间"},
    })
    for _, want := range []string{
        "| 问题分解 | - |",
        "| 查询扩展 | - |",
        "| 最终回答 | `gpt-5.6` |",
        "| 1 | 张三 \\| 旅行<br>时间 |",
    } {
        if !strings.Contains(got, want) {
            t.Fatalf("meta missing %q: %s", want, got)
        }
    }
}
```

- [ ] **Step 3: Run the focused test to verify the old formatter fails**

Run:

```sh
cd feishu-bot
TMPDIR="$PWD/.tmp-go" GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go test . -run 'TestFormatAnswerRunMeta' -count=1
```

Expected: FAIL because the old one-line labels do not contain the new headings and table rows.

### Task 2: Implement escaped Markdown table rendering

**Files:**
- Modify: `feishu-bot/bot.go:905-938`
- Test: `feishu-bot/ai_test.go:188-235`

- [ ] **Step 1: Add a cell escaping helper beside `formatAnswerRunMeta`**

```go
func escapeMarkdownTableCell(value string) string {
    value = strings.TrimSpace(value)
    value = strings.ReplaceAll(value, "\\", "\\\\")
    value = strings.ReplaceAll(value, "|", "\\|")
    return strings.ReplaceAll(value, "\n", "<br>")
}
```

- [ ] **Step 2: Replace the one-line formatter with the three table sections**

```go
func formatAnswerRunMeta(meta answerRunMeta) string {
    model := func(value string) string {
        if strings.TrimSpace(value) == "" {
            return "-"
        }
        return "`" + escapeMarkdownTableCell(value) + "`"
    }

    lines := []string{
        "> **模型**",
        "| 步骤 | 模型 |",
        "| --- | --- |",
        "| 问题分解 | " + model(meta.Models.QueryDecomposition) + " |",
        "| 查询扩展 | " + model(meta.Models.QueryExpansion) + " |",
        "| 最终回答 | " + model(meta.Models.FinalAnswer) + " |",
    }

    // Append a decomposition table only when it has at least one populated row.
    // Append each non-empty expanded query in its own numbered table row.
    return strings.Join(lines, "\n")
}
```

Build decomposition rows from `Entities`, `Concepts`, and the non-empty time range. Join each list with `、`, escape each value, and use `TimeFrom + " ~ " + TimeTo` after omitting the empty side. Skip blank expanded-query entries while numbering retained rows from `1`.

- [ ] **Step 3: Run the focused tests to verify the implementation passes**

Run:

```sh
cd feishu-bot
TMPDIR="$PWD/.tmp-go" GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go test . -run 'TestFormatAnswerRunMeta' -count=1
```

Expected: PASS for the normal three-table output and escaped model-provided query text.

- [ ] **Step 4: Format the changed Go files**

Run:

```sh
/volume4/homes/wangxuanrun/.local/go/bin/gofmt -w feishu-bot/bot.go feishu-bot/ai_test.go
```

### Task 3: Run required verification and commit

**Files:**
- Modify: `feishu-bot/bot.go`
- Modify: `feishu-bot/ai_test.go`

- [ ] **Step 1: Run the Feishu bot test suite**

Run:

```sh
cd feishu-bot
TMPDIR="$PWD/.tmp-go" GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go test ./...
```

Expected: PASS.

- [ ] **Step 2: Run repository-required backend build**

Run:

```sh
cd backend
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go build -o /dev/null .
```

Expected: exit status 0.

- [ ] **Step 3: Run repository-required frontend type check**

Run:

```sh
cd frontend
/volume4/@appstore/Node.js_v18/usr/local/bin/node ./node_modules/typescript/bin/tsc --noEmit
```

Expected: exit status 0.

- [ ] **Step 4: Inspect the final diff and commit only the formatter and test**

Run:

```sh
git diff --check
git add feishu-bot/bot.go feishu-bot/ai_test.go
git commit -m "feat(feishu): tabulate answer run metadata"
```

Expected: clean diff check and a commit containing only the formatter and its tests. Do not stage `.superpowers/` preview artifacts or plan/spec documents already committed separately.
