package main

import "testing"

func TestInitLLMApiLogTableAddsFirstTokenColumnToExistingTable(t *testing.T) {
	withPromptTemplateTestDB(t)
	if _, err := aiDB.Exec(`CREATE TABLE llm_api_logs (
		id INTEGER PRIMARY KEY,
		timestamp TEXT NOT NULL,
		method TEXT NOT NULL,
		url TEXT NOT NULL,
		provider TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		feature TEXT NOT NULL DEFAULT '',
		request_body TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 0,
		response_body TEXT NOT NULL DEFAULT '',
		duration_ms INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("create legacy log table: %v", err)
	}

	if err := initLLMApiLogTable(); err != nil {
		t.Fatalf("upgrade log table: %v", err)
	}

	rows, err := aiDB.Query(`PRAGMA table_info(llm_api_logs)`)
	if err != nil {
		t.Fatalf("read log table schema: %v", err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan log table column: %v", err)
		}
		if name == "first_token_ms" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate log table schema: %v", err)
	}
	if !found {
		t.Fatal("first_token_ms column was not added")
	}
}
