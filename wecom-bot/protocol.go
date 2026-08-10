package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	commandSubscribe       = "aibot_subscribe"
	commandMessageCallback = "aibot_msg_callback"
	commandRespondMessage  = "aibot_respond_msg"
	chatTypeGroup          = "group"
	messageTypeText        = "text"
)

type commandHeaders struct {
	RequestID string `json:"req_id"`
}

type commandFrame struct {
	Command   string          `json:"cmd"`
	Headers   commandHeaders  `json:"headers"`
	Body      json.RawMessage `json:"body"`
	ErrorCode int             `json:"errcode,omitempty"`
	ErrorMsg  string          `json:"errmsg,omitempty"`
}

type incomingGroupText struct {
	RequestID string
	MessageID string
	ChatID    string
	UserID    string
	Content   string
}

type streamReplyBody struct {
	MessageType string `json:"msgtype"`
	Stream      struct {
		ID      string `json:"id"`
		Finish  bool   `json:"finish"`
		Content string `json:"content"`
	} `json:"stream"`
}

type messageCallbackBody struct {
	MessageID string `json:"msgid"`
	ChatID    string `json:"chatid"`
	ChatType  string `json:"chattype"`
	From      struct {
		UserID string `json:"userid"`
	} `json:"from"`
	MessageType string `json:"msgtype"`
	Text        struct {
		Content string `json:"content"`
	} `json:"text"`
}

// decodeIncomingGroupText keeps the platform boundary strict: this gateway
// deliberately accepts only internal group text callbacks.
func decodeIncomingGroupText(raw []byte) (incomingGroupText, bool, error) {
	var frame commandFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return incomingGroupText{}, false, err
	}
	if frame.Command != commandMessageCallback {
		return incomingGroupText{}, false, nil
	}

	var body messageCallbackBody
	if err := json.Unmarshal(frame.Body, &body); err != nil {
		return incomingGroupText{}, false, err
	}
	if body.ChatType != chatTypeGroup || body.MessageType != messageTypeText {
		return incomingGroupText{}, false, nil
	}
	if frame.Headers.RequestID == "" || body.MessageID == "" || body.ChatID == "" || body.From.UserID == "" || strings.TrimSpace(body.Text.Content) == "" {
		return incomingGroupText{}, false, nil
	}
	return incomingGroupText{
		RequestID: frame.Headers.RequestID,
		MessageID: body.MessageID,
		ChatID:    body.ChatID,
		UserID:    body.From.UserID,
		Content:   body.Text.Content,
	}, true, nil
}

func newSubscribe(botID, secret, requestID string) commandFrame {
	return commandFrame{
		Command: commandSubscribe,
		Headers: commandHeaders{RequestID: requestID},
		Body: mustJSON(map[string]string{
			"bot_id": botID,
			"secret": secret,
		}),
	}
}

func newStreamReply(requestID, streamID, content string, finish bool) commandFrame {
	body := streamReplyBody{MessageType: "stream"}
	body.Stream.ID = streamID
	body.Stream.Content = content
	body.Stream.Finish = finish
	return commandFrame{
		Command: commandRespondMessage,
		Headers: commandHeaders{RequestID: requestID},
		Body:    mustJSON(body),
	}
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func newID() string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", bytes)
}
