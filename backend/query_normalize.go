package main

import (
	"sort"
	"strings"

	"welink/backend/service"
)

// replaceAllFold 在 s 中把 old（忽略大小写）全部替换为 new，保留原文其余部分的大小写。
func replaceAllFold(s, old, new string) string {
	if old == "" {
		return s
	}
	sl := strings.ToLower(s)
	ol := strings.ToLower(old)
	var out strings.Builder
	i := 0
	for {
		idx := strings.Index(sl[i:], ol)
		if idx < 0 {
			out.WriteString(s[i:])
			break
		}
		idx += i
		out.WriteString(s[i:idx])
		out.WriteString(new)
		i = idx + len(old)
	}
	return out.String()
}

// normalizeEntityNames 把原文提问里的联系人外号/简称还原为真实姓名。
//
// 在 memory-search 最前面执行一次，让后续的查询分解、实体解析、检索、
// 查询扩展和最终回答全部都围绕实体的原名进行，避免“外号只在扩展里还原、
// 实体却在最终链路里走丢”的问题。
func normalizeEntityNames(query string, svc *service.ContactService) string {
	query = strings.TrimSpace(query)
	if query == "" || svc == nil {
		return query
	}
	all, _ := GetAllContactAliases()
	if len(all) == 0 {
		return query
	}

	// contact_key -> 展示名（优先备注，其次昵称）
	nameOf := make(map[string]string)
	for _, c := range svc.GetCachedStats() {
		if strings.HasSuffix(c.Username, "@chatroom") || strings.HasPrefix(c.Username, "gh_") {
			continue
		}
		key := "contact:" + c.Username
		name := c.Remark
		if name == "" {
			name = c.Nickname
		}
		if name != "" {
			nameOf[key] = name
		}
	}
	for _, ec := range extraContactsBySvc(svc) {
		if ec.DisplayName != "" {
			nameOf[ec.ContactKey] = ec.DisplayName
		}
	}

	type repl struct {
		alias string
		name  string
	}
	var repls []repl
	qLow := strings.ToLower(query)
	for key, aliases := range all {
		name := nameOf[key]
		if name == "" {
			continue
		}
		for _, a := range aliases {
			a = strings.TrimSpace(a)
			if len([]rune(a)) < 2 {
				continue // 单字外号太容易误伤普通语句，不替换
			}
			an := normalizeAlias(a)
			if an == "" || an == strings.ToLower(name) {
				continue // 外号就是原名时无需替换
			}
			if strings.Contains(qLow, an) {
				repls = append(repls, repl{alias: a, name: name})
			}
		}
	}
	if len(repls) == 0 {
		return query
	}

	// 长外号优先，避免“dj”在“dj坤”里被提前替换。
	sort.Slice(repls, func(i, j int) bool {
		return len([]rune(repls[i].alias)) > len([]rune(repls[j].alias))
	})
	out := query
	for _, r := range repls {
		out = replaceAllFold(out, r.alias, r.name)
	}
	return out
}
