package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDispatchRejectsUnlistedInternalGroupUser(t *testing.T) {
	sent := make(chan commandFrame, 1)
	b := newBot(&Config{AllowedUsers: []string{"alice"}}, func(frame commandFrame) error {
		sent <- frame
		return nil
	})
	b.answer = func(context.Context, string, []llmMessage) (string, error) {
		t.Fatal("answer pipeline must not run for an unlisted user")
		return "", nil
	}

	b.dispatch(incomingGroupText{RequestID: "req_1", MessageID: "msg_1", ChatID: "chat_1", UserID: "bob", Content: "@机器人 你好"})

	select {
	case frame := <-sent:
		var body streamReplyBody
		if err := json.Unmarshal(frame.Body, &body); err != nil {
			t.Fatalf("decode reply: %v", err)
		}
		if !body.Stream.Finish || body.Stream.Content != "你没有权限使用本机器人。" {
			t.Fatalf("unexpected permission reply: %#v", body)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for permission reply")
	}
}

func TestDispatchUsesGroupAndUserForSessionIsolation(t *testing.T) {
	sent := make(chan commandFrame, 2)
	b := newBot(&Config{BotName: "机器人"}, func(frame commandFrame) error {
		sent <- frame
		return nil
	})
	seenKey := make(chan string, 1)
	b.answer = func(_ context.Context, key string, history []llmMessage) (string, error) {
		seenKey <- key
		if len(history) != 1 || history[0] != (llmMessage{Role: "user", Content: "查询张三"}) {
			t.Fatalf("answer history = %#v", history)
		}
		return "回答", nil
	}

	b.dispatch(incomingGroupText{RequestID: "req_1", MessageID: "msg_1", ChatID: "chat_1", UserID: "alice", Content: "@机器人 查询张三"})

	select {
	case key := <-seenKey:
		if key != "wecom:group:chat_1:alice" {
			t.Fatalf("session key = %q", key)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for answer pipeline")
	}
}
