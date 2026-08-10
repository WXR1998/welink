package main

import (
	"encoding/json"
	"testing"
)

func TestDecodeIncomingGroupTextAcceptsInternalGroupText(t *testing.T) {
	msg, ok, err := decodeIncomingGroupText([]byte(`{
		"cmd": "aibot_msg_callback",
		"headers": {"req_id": "req_1"},
		"body": {
			"msgid": "msg_1",
			"chatid": "chat_1",
			"chattype": "group",
			"from": {"userid": "zhangsan"},
			"msgtype": "text",
			"text": {"content": "@机器人 你好"}
		}
	}`))
	if err != nil {
		t.Fatalf("decode callback: %v", err)
	}
	if !ok {
		t.Fatal("expected internal group text callback to be accepted")
	}
	if msg.RequestID != "req_1" || msg.MessageID != "msg_1" || msg.ChatID != "chat_1" || msg.UserID != "zhangsan" || msg.Content != "@机器人 你好" {
		t.Fatalf("unexpected message: %#v", msg)
	}
}

func TestDecodeIncomingGroupTextRejectsUnsupportedCallbacks(t *testing.T) {
	cases := []string{
		`{"cmd":"aibot_event_callback","headers":{"req_id":"req"},"body":{}}`,
		`{"cmd":"aibot_msg_callback","headers":{"req_id":"req"},"body":{"chattype":"single","msgtype":"text","text":{"content":"hi"}}}`,
		`{"cmd":"aibot_msg_callback","headers":{"req_id":"req"},"body":{"chattype":"group","msgtype":"image"}}`,
	}
	for _, raw := range cases {
		if _, ok, err := decodeIncomingGroupText([]byte(raw)); err != nil || ok {
			t.Fatalf("callback must be ignored, ok=%v err=%v raw=%s", ok, err, raw)
		}
	}
}

func TestNewStreamReplyUsesOriginalRequestAndStreamIDs(t *testing.T) {
	frame := newStreamReply("req_1", "stream_1", "正在查询...", false)
	if frame.Command != commandRespondMessage || frame.Headers.RequestID != "req_1" {
		t.Fatalf("unexpected reply frame: %#v", frame)
	}

	var body streamReplyBody
	if err := json.Unmarshal(frame.Body, &body); err != nil {
		t.Fatalf("decode stream reply: %v", err)
	}
	if body.MessageType != "stream" || body.Stream.ID != "stream_1" || body.Stream.Content != "正在查询..." || body.Stream.Finish {
		t.Fatalf("unexpected stream reply body: %#v", body)
	}
}
