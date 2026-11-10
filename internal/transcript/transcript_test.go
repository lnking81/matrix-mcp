package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	matrixclient "github.com/ricelines/matrix-mcp/internal/matrix"
)

func sampleExport() matrixclient.ExportResult {
	day := time.Date(2026, 11, 9, 15, 58, 0, 0, time.FixedZone("WITA", 8*3600))
	at := func(d time.Duration) int64 { return day.Add(d).UnixMilli() }
	return matrixclient.ExportResult{
		RoomID:        "!room:example.com",
		RoomName:      "Алексей (WA)",
		EventsScanned: 9,
		StopReason:    matrixclient.ExportStopRoomStart,
		Complete:      true,
		Senders:       map[string][]string{"Me": {"@me:example.com"}, "Алексей": {"@lid:example.com", "@phone:example.com"}},
		Omitted:       map[string]int{"m.reaction (withdrawn)": 1},
		Messages: []matrixclient.CompactMessage{
			{EventID: "$1", From: "Me", Text: "line one\nline two", Edited: true, TimestampMS: at(0),
				Reactions: map[string][]string{"👍": {"Алексей"}}},
			{EventID: "$2", From: "Алексей", Text: "ответ", ReplyTo: "$1", TimestampMS: at(3 * time.Minute)},
			{EventID: "$3", From: "Алексей", Kind: "voice", TimestampMS: at(10 * time.Hour),
				Media: &matrixclient.CompactMedia{MIMEType: "audio/ogg", DurationMS: 31500}},
			{EventID: "$4", From: "Me", Kind: "deleted", TimestampMS: at(10*time.Hour + time.Minute)},
			{EventID: "$5", From: "Me", Kind: "undecryptable", Error: "room key not available", TimestampMS: at(10*time.Hour + 2*time.Minute)},
			{EventID: "$6", From: "Алексей", ReplyTo: "$1", Text: "late reply", TimestampMS: at(10*time.Hour + 3*time.Minute)},
		},
	}
}

func TestTextTranscript(t *testing.T) {
	loc := time.FixedZone("WITA", 8*3600)
	out := string(Text(sampleExport(), loc, time.Date(2026, 11, 10, 9, 0, 0, 0, loc)))
	for _, want := range []string{
		"# Matrix room export: Алексей (WA) (!room:example.com)\n",
		"# Period: 2026-11-09 15:58 – 2026-11-10 02:01\n",
		"# 6 messages from 9 events. Complete: reached the start of the room.\n",
		"# Senders: Me = @me:example.com; Алексей = @lid:example.com, @phone:example.com\n",
		"# Left out: m.reaction (withdrawn) 1\n",
		"\n== 2026-11-09 Mon ==\n[15:58] Me: line one\n    line two (edited)\n    reactions: 👍 Алексей\n",
		"[16:01] Алексей (↩ 15:58 Me «line one line two (edited)»): ответ\n",
		"\n== 2026-11-10 Tue ==\n[01:58] Алексей: [voice 0:32 $3]\n",
		"[01:59] Me: [deleted]\n",
		"[02:00] Me: [undecryptable: room key not available]\n",
		"[02:01] Алексей (↩ 09.11 15:58 Me «line one line two (edited)»): late reply\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript lacks %q\n---\n%s", want, out)
		}
	}
}

func TestTextTranscriptMarksIncompleteExport(t *testing.T) {
	res := sampleExport()
	res.StopReason = matrixclient.ExportStopMaxEvents
	res.Complete = false
	if out := string(Text(res, time.UTC, time.Now())); !strings.Contains(out, "INCOMPLETE: stopped at max_events") {
		t.Fatalf("transcript does not say it is incomplete:\n%s", out)
	}
}

func TestJSONL(t *testing.T) {
	data, err := JSONL(sampleExport(), time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("JSONL() error = %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 7 {
		t.Fatalf("lines = %d, want header + 6", len(lines))
	}
	var header map[string]any
	if err := json.Unmarshal(lines[0], &header); err != nil {
		t.Fatalf("header: %v", err)
	}
	if header["room_id"] != "!room:example.com" || header["messages"] != float64(6) || header["complete"] != true || header["generated"] != "2026-11-10T01:00:00Z" {
		t.Fatalf("header = %#v", header)
	}
	var first map[string]any
	if err := json.Unmarshal(lines[1], &first); err != nil {
		t.Fatalf("message: %v", err)
	}
	if first["event_id"] != "$1" || first["text"] != "line one\nline two" || first["edited"] != true {
		t.Fatalf("first message = %#v", first)
	}
	if _, ok := first["TimestampMS"]; ok {
		t.Fatal("internal fields leak into JSONL")
	}
}

func TestStoreExpiryAndEviction(t *testing.T) {
	now := time.Unix(1000, 0)
	store := NewStore(time.Minute, 10, 15)
	store.now = func() time.Time { return now }

	if _, _, err := store.Put(File{Data: make([]byte, 11)}); err != ErrTooLarge {
		t.Fatalf("Put(11 bytes) error = %v, want ErrTooLarge", err)
	}
	first, _, err := store.Put(File{Name: "a", Data: make([]byte, 8)})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	second, _, err := store.Put(File{Name: "b", Data: make([]byte, 8)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(first); ok {
		t.Fatal("oldest export not evicted to stay under the total limit")
	}
	if f, ok := store.Get(second); !ok || f.Name != "b" {
		t.Fatal("newest export missing")
	}
	now = now.Add(time.Minute)
	if _, ok := store.Get(second); ok {
		t.Fatal("export still served after expiry")
	}
}
