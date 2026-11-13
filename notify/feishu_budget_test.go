package notify

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestFeishuCompleteBatchCanFitAfterPrefixExceedsBudget(t *testing.T) {
	m := Message{Batch: true, Items: []Item{{Name: "a"}, {Name: "b"}}, HomeURL: "https://example.invalid/"}
	full, _ := json.Marshal(map[string]any{"msg_type": "interactive", "card": feishuCard(m, 2)})
	m.HomeURL += strings.Repeat("a", feishuMaxRequestBytes-len(full))
	full, _ = json.Marshal(map[string]any{"msg_type": "interactive", "card": feishuCard(m, 2)})
	prefix, _ := json.Marshal(map[string]any{"msg_type": "interactive", "card": feishuCard(m, 1)})
	if len(full) > feishuMaxRequestBytes || len(prefix) <= feishuMaxRequestBytes {
		t.Fatalf("前提不成立: full=%d prefix=%d", len(full), len(prefix))
	}
	t.Logf("full=%d prefix=%d", len(full), len(prefix))
	_, kept, err := feishuPayload(m, "", "")
	if err != nil || kept != 2 {
		t.Fatalf("完整批次可发送: kept=%d err=%v", kept, err)
	}
}

func TestFeishuSerializedRequestBudget(t *testing.T) {
	for _, token := range []string{"a", "<>&", "\"", "\\", "中"} {
		for _, signed := range []bool{false, true} {
			m := Message{Batch: true, WindowMinutes: 60, HomeURL: "https://example.invalid/" + strings.Repeat("<&", 300)}
			for i := 0; i < 100; i++ {
				m.Items = append(m.Items, Item{Name: strings.Repeat(token, 300), Severity: "high"})
			}
			before, _ := json.Marshal(m)
			ts, sig := "", ""
			if signed {
				ts = "1794538800"
				sig = feishuSign(ts, "declared-nonsecret-test-input")
				if len(sig) != 44 {
					t.Fatal(len(sig))
				}
			}
			p, k, e := feishuPayload(m, ts, sig)
			if e != nil {
				t.Fatal(e)
			}
			raw, e := json.Marshal(p)
			if e != nil {
				t.Fatal(e)
			}
			if len(raw) > feishuMaxRequestBytes || k < 1 || k >= len(m.Items) {
				t.Fatalf("token=%q signed=%v bytes=%d kept=%d", token, signed, len(raw), k)
			}
			if signed {
				if p["timestamp"] != ts || p["sign"] != sig {
					t.Fatal("签名字段改变")
				}
			} else if _, ok := p["sign"]; ok {
				t.Fatal("未配置签名")
			}
			if !reflect.DeepEqual(p["card"], feishuCard(m, k)) {
				t.Fatal("条目前缀不一致")
			}
			next := map[string]any{"msg_type": "interactive", "card": feishuCard(m, k+1)}
			if signed {
				next["timestamp"] = ts
				next["sign"] = sig
			}
			nextRaw, _ := json.Marshal(next)
			if len(nextRaw) <= feishuMaxRequestBytes {
				t.Fatal("仍可装入下一条")
			}
			p2, k2, e := feishuPayload(m, ts, sig)
			if e != nil || k2 != k || !reflect.DeepEqual(p, p2) {
				t.Fatal("重复构造不一致")
			}
			after, _ := json.Marshal(m)
			if string(before) != string(after) {
				t.Fatal("输入被修改")
			}
			t.Logf("token=%q signed=%v kept=%d bytes=%d", token, signed, k, len(raw))
		}
	}
}

func TestFeishuUnsendableRequest(t *testing.T) {
	for _, m := range []Message{
		{},
		{Items: []Item{{Name: strings.Repeat("<", 20000)}}},
		{Batch: true, Items: []Item{{Name: strings.Repeat("a", 30000)}, {Name: "后续条目"}}},
		{Batch: true, Items: []Item{{Name: "普通条目"}}, HomeURL: "https://example.invalid/" + strings.Repeat("<", 20000)},
	} {
		p, k, e := feishuPayload(m, "", "")
		if e == nil || k != 0 || p != nil {
			t.Fatalf("无可投递内容应失败: kept=%d err=%v", k, e)
		}
	}
	m := Message{Items: []Item{{Name: "正常漏洞", Severity: "high", DetailURL: "https://example.invalid/"}}}
	p, k, e := feishuPayload(m, "", "")
	if e != nil || k != 1 || !reflect.DeepEqual(p["card"], feishuCard(m, 1)) {
		t.Fatal("普通单条消息异常")
	}
}
