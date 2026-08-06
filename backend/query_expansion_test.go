package main

import (
	"strings"
	"testing"
)

func TestBuildQueryExpansionPromptRequiresAliasNormalization(t *testing.T) {
	p := buildQueryExpansionPrompt(nil)
	// 空库时提示仍应要求还原外号为原名。
	for _, want := range []string{
		"必须依据下面的映射关系把它还原为对应的真实姓名",
		"不要使用外号/简称",
		"查询扩展助手",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("query expansion prompt missing %q: %s", want, p)
		}
	}
}
