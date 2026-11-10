package matrix

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func redactedEncryptedPayload(eventID string, ts int64) map[string]any {
	return map[string]any{
		"event_id":         eventID,
		"room_id":          "!room:example.com",
		"sender":           "@alice:example.com",
		"type":             event.EventEncrypted.Type,
		"origin_server_ts": ts,
		"content":          map[string]any{},
		"unsigned": map[string]any{
			"redacted_because": map[string]any{
				"event_id":         "$redaction",
				"room_id":          "!room:example.com",
				"sender":           "@alice:example.com",
				"type":             "m.room.redaction",
				"origin_server_ts": ts + 1,
				"redacts":          eventID,
				"content":          map[string]any{},
			},
		},
	}
}

func encryptedPayloadAt(eventID, sessionID string, ts int64) map[string]any {
	payload := encryptedEventPayload(eventID)
	payload["origin_server_ts"] = ts
	payload["content"].(map[string]any)["session_id"] = sessionID
	return payload
}

func messagesResponder(t *testing.T, chunk ...map[string]any) func(*http.Request) *http.Response {
	return func(r *http.Request) *http.Response {
		if r.URL.EscapedPath() != "/_matrix/client/v3/rooms/%21room:example.com/messages" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.EscapedPath())
		}
		items := make([]any, 0, len(chunk))
		for _, evt := range chunk {
			items = append(items, evt)
		}
		return jsonResponse(t, r, http.StatusOK, map[string]any{"start": "s", "end": "e", "chunk": items})
	}
}

// A redacted encrypted event has content {} and no algorithm. It used to
// fail the whole page with "unsupported event encryption algorithm".
func TestListMessagesReturnsRedactedEncryptedEventWithoutDecrypting(t *testing.T) {
	svc := newClientTestService(t, messagesResponder(t,
		encryptedPayloadAt("$enc", "session-id", 2000),
		redactedEncryptedPayload("$gone", 1000),
	))
	svc.cryptoHelper = stubCryptoHelper{
		decrypt: func(ctx context.Context, evt *event.Event) (*event.Event, error) {
			if evt.ID == "$gone" {
				t.Fatal("Decrypt() called for a redacted event")
			}
			return decryptedMessageEvent(evt, "still here"), nil
		},
	}

	result, err := svc.ListMessages(context.Background(), ListMessagesRequest{RoomID: "!room:example.com", Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(result.Events) != 2 || result.End != "e" {
		t.Fatalf("ListMessages() = %#v, want both events and the pagination token", result)
	}
	gone := result.Events[0]
	if gone.EventID != "$gone" || !gone.Redacted || gone.RedactedBy != "@alice:example.com" || gone.Type != event.EventEncrypted.Type {
		t.Fatalf("redacted event = %#v", gone)
	}
	if result.Events[1].Content["body"] != "still here" {
		t.Fatalf("decrypted event = %#v", result.Events[1])
	}
}

func TestListMessagesUndecryptableEventsSkippedOrPlaceholders(t *testing.T) {
	newService := func() *Service {
		svc := newClientTestService(t, messagesResponder(t,
			encryptedPayloadAt("$bad", "session-id", 1000),
			encryptedPayloadAt("$good", "session-id", 2000),
		))
		svc.cryptoHelper = stubCryptoHelper{
			decrypt: func(ctx context.Context, evt *event.Event) (*event.Event, error) {
				if evt.ID == "$bad" {
					return nil, errors.New("decrypt exploded")
				}
				return decryptedMessageEvent(evt, "fine"), nil
			},
		}
		return svc
	}

	// Default (upstream): skipped, and no longer an error for the page.
	result, err := newService().ListMessages(context.Background(), ListMessagesRequest{RoomID: "!room:example.com"})
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(result.Events) != 1 || result.Events[0].EventID != "$good" {
		t.Fatalf("ListMessages() events = %#v, want only $good", result.Events)
	}

	result, err = newService().ListMessages(context.Background(), ListMessagesRequest{RoomID: "!room:example.com", IncludeUndecryptable: true})
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(result.Events) != 2 {
		t.Fatalf("ListMessages() events = %#v, want a placeholder and $good", result.Events)
	}
	bad := result.Events[0]
	if bad.EventID != "$bad" || bad.DecryptionError != "decrypt exploded" || bad.Content != nil {
		t.Fatalf("placeholder = %#v", bad)
	}
}

func TestMissingSessionIsWaitedForOnce(t *testing.T) {
	waits := 0
	svc := newClientTestService(t, messagesResponder(t,
		encryptedPayloadAt("$a", "old-session", 1000),
		encryptedPayloadAt("$b", "old-session", 2000),
		encryptedPayloadAt("$c", "old-session", 3000),
	))
	svc.cryptoHelper = stubCryptoHelper{
		decrypt: func(context.Context, *event.Event) (*event.Event, error) {
			return nil, cryptohelper.NoSessionFound
		},
		waitForSession: func(context.Context, id.RoomID, id.SenderKey, id.SessionID, time.Duration) bool {
			waits++
			return false
		},
	}

	result, err := svc.ListMessages(context.Background(), ListMessagesRequest{RoomID: "!room:example.com", IncludeUndecryptable: true})
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if waits != 1 {
		t.Fatalf("waited for the session %d times, want 1", waits)
	}
	if len(result.Events) != 3 || result.Events[2].DecryptionError != "room key not available" {
		t.Fatalf("events = %#v, want three placeholders", result.Events)
	}
}

func msgEvent(eventID, sender string, ts int64, content map[string]any) decodedEvent {
	return decodedEvent{evt: &event.Event{
		ID:        id.EventID(eventID),
		RoomID:    "!room:example.com",
		Sender:    id.UserID(sender),
		Type:      event.EventMessage,
		Timestamp: ts,
		Content:   event.Content{Raw: content},
	}}
}

func TestCompactEvents(t *testing.T) {
	names := map[id.UserID]string{"@me:example.com": "Me", "@bob:example.com": "Bob"}
	reaction := decodedEvent{evt: &event.Event{
		ID: "$r", Sender: "@bob:example.com", Type: event.EventReaction, Timestamp: 2500,
		Content: event.Content{Raw: map[string]any{"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": "$t1", "key": "👍"}}},
	}}
	orphanReaction := decodedEvent{evt: &event.Event{
		ID: "$r2", Sender: "@bob:example.com", Type: event.EventReaction, Timestamp: 2600,
		Content: event.Content{Raw: map[string]any{"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": "$older", "key": "❤️"}}},
	}}
	decoded := []decodedEvent{
		// Out of order on purpose: compactEvents sorts by time.
		msgEvent("$edit", "@me:example.com", 3000, map[string]any{
			"msgtype": "m.text", "body": "* fixed",
			"m.new_content": map[string]any{"msgtype": "m.text", "body": "fixed text", "formatted_body": "<p>fixed text</p>"},
			"m.relates_to":  map[string]any{"rel_type": "m.replace", "event_id": "$t1"},
		}),
		msgEvent("$t1", "@me:example.com", 1000, map[string]any{
			"msgtype": "m.text", "body": "first", "format": "org.matrix.custom.html", "formatted_body": "<p>first</p>",
			"com.beeper.linkpreviews": []any{},
		}),
		reaction,
		orphanReaction,
		msgEvent("$reply", "@bob:example.com", 2000, map[string]any{
			"msgtype": "m.text", "body": "> <@me:example.com> first\n\nanswer",
			"m.relates_to": map[string]any{"m.in_reply_to": map[string]any{"event_id": "$t1"}},
		}),
		msgEvent("$voice", "@bob:example.com", 4000, map[string]any{
			"msgtype": "m.audio", "body": "Voice message.ogg", "url": "mxc://x/y",
			"info":                     map[string]any{"mimetype": "audio/ogg", "size": 51234, "duration": 31500},
			"org.matrix.msc3245.voice": map[string]any{},
		}),
		msgEvent("$photo", "@bob:example.com", 5000, map[string]any{
			"msgtype": "m.image", "body": "look at this", "filename": "IMG_1.jpg",
			"info": map[string]any{"mimetype": "image/jpeg", "size": 2048},
		}),
		msgEvent("$hijack", "@bob:example.com", 5500, map[string]any{
			"msgtype": "m.text", "body": "* hijacked",
			"m.new_content": map[string]any{"msgtype": "m.text", "body": "hijacked"},
			"m.relates_to":  map[string]any{"rel_type": "m.replace", "event_id": "$t1"},
		}),
		{evt: &event.Event{ID: "$del", Sender: "@bob:example.com", Type: event.EventEncrypted, Timestamp: 6000}, redacted: true},
		{evt: &event.Event{ID: "$enc", Sender: "@bob:example.com", Type: event.EventEncrypted, Timestamp: 7000}, decryptErr: errEventContentUnavailable},
	}

	loc := time.FixedZone("WITA", 8*3600)
	messages, omitted := compactEvents(decoded, names, loc)
	if omitted != nil {
		t.Fatalf("omitted = %#v, want none", omitted)
	}
	byID := map[string]CompactMessage{}
	var order []string
	for _, msg := range messages {
		byID[msg.EventID] = msg
		order = append(order, msg.EventID)
	}
	if got := strings.Join(order, ","); got != "$t1,$reply,$r2,$voice,$photo,$hijack,$del,$enc" {
		t.Fatalf("order = %s", got)
	}

	first := byID["$t1"]
	if first.Text != "fixed text" || !first.Edited || first.From != "Me" || first.Time != "1970-01-01T08:00:01+08:00" {
		t.Fatalf("$t1 = %#v, want the edit applied in local time", first)
	}
	if got := first.Reactions["👍"]; len(got) != 1 || got[0] != "Bob" {
		t.Fatalf("$t1 reactions = %#v", first.Reactions)
	}
	if reply := byID["$reply"]; reply.Text != "answer" || reply.ReplyTo != "$t1" {
		t.Fatalf("$reply = %#v, want the fallback stripped", reply)
	}
	if orphan := byID["$r2"]; orphan.Kind != "reaction" || orphan.Text != "❤️" || orphan.ReactionTo != "$older" {
		t.Fatalf("$r2 = %#v", orphan)
	}
	voice := byID["$voice"]
	if voice.Kind != "voice" || voice.Text != "" || voice.Media == nil || voice.Media.DurationMS != 31500 || voice.Media.FileName != "" {
		t.Fatalf("$voice = %#v / %#v", voice, voice.Media)
	}
	photo := byID["$photo"]
	if photo.Kind != "image" || photo.Text != "look at this" || photo.Media.FileName != "IMG_1.jpg" {
		t.Fatalf("$photo = %#v", photo)
	}
	// Bob cannot edit Me's message: kept as a separate edit entry.
	if hijack := byID["$hijack"]; hijack.Kind != "edit" || hijack.EditOf != "$t1" || hijack.Text != "hijacked" {
		t.Fatalf("$hijack = %#v", hijack)
	}
	if del := byID["$del"]; del.Kind != "deleted" || del.Text != "" {
		t.Fatalf("$del = %#v", del)
	}
	if enc := byID["$enc"]; enc.Kind != "undecryptable" || enc.Error != "room key not available" {
		t.Fatalf("$enc = %#v", enc)
	}
}

func TestListMessagesCompactFiltersAndNamesSenders(t *testing.T) {
	svc := newClientTestService(t, func(r *http.Request) *http.Response {
		switch r.URL.EscapedPath() {
		case "/_matrix/client/v3/rooms/%21room:example.com/messages":
			if filter := r.URL.Query().Get("filter"); !strings.Contains(filter, `"types":["m.room.message","m.room.encrypted","m.sticker","m.reaction"]`) {
				t.Fatalf("filter = %q", filter)
			}
			return jsonResponse(t, r, http.StatusOK, map[string]any{"start": "s", "end": "e", "chunk": []any{
				map[string]any{"event_id": "$1", "room_id": "!room:example.com", "sender": "@ghost:example.com", "type": "m.room.message",
					"origin_server_ts": 1000, "content": map[string]any{"msgtype": "m.text", "body": "hi"}},
				map[string]any{"event_id": "$2", "room_id": "!room:example.com", "sender": "@left:example.com", "type": "m.room.message",
					"origin_server_ts": 2000, "content": map[string]any{"msgtype": "m.text", "body": "bye"}},
			}})
		case "/_matrix/client/v3/rooms/%21room:example.com/joined_members":
			return jsonResponse(t, r, http.StatusOK, map[string]any{"joined": map[string]any{
				"@ghost:example.com": map[string]any{"display_name": "Алексей"},
			}})
		case "/_matrix/client/v3/profile/@left:example.com":
			return jsonResponse(t, r, http.StatusNotFound, map[string]any{"errcode": "M_NOT_FOUND"})
		}
		t.Fatalf("unexpected %s %s", r.Method, r.URL.EscapedPath())
		return nil
	})

	result, err := svc.ListMessagesCompact(context.Background(), ListMessagesRequest{RoomID: "!room:example.com", Limit: 10})
	if err != nil {
		t.Fatalf("ListMessagesCompact() error = %v", err)
	}
	if len(result.Messages) != 2 || result.Messages[0].From != "Алексей" || result.Messages[1].From != "left" || result.End != "e" || result.Timezone != "UTC" {
		t.Fatalf("result = %#v", result)
	}
	if ids := result.Senders["Алексей"]; len(ids) != 1 || ids[0] != "@ghost:example.com" {
		t.Fatalf("senders = %#v", result.Senders)
	}
}

func TestExportTimelinePagesUntilSince(t *testing.T) {
	pages := 0
	svc := newClientTestService(t, func(r *http.Request) *http.Response {
		path := r.URL.EscapedPath()
		switch {
		case path == "/_matrix/client/v3/rooms/%21room:example.com/messages":
			pages++
			msg := func(eventID string, ts int64) map[string]any {
				return map[string]any{"event_id": eventID, "room_id": "!room:example.com", "sender": "@me:example.com",
					"type": "m.room.message", "origin_server_ts": ts, "content": map[string]any{"msgtype": "m.text", "body": eventID}}
			}
			switch r.URL.Query().Get("from") {
			case "":
				return jsonResponse(t, r, http.StatusOK, map[string]any{"start": "a", "end": "b", "chunk": []any{msg("$4", 4000), msg("$3", 3000)}})
			case "b":
				// An empty page with a token is not the end of the room.
				return jsonResponse(t, r, http.StatusOK, map[string]any{"start": "b", "end": "c", "chunk": []any{}})
			case "c":
				return jsonResponse(t, r, http.StatusOK, map[string]any{"start": "c", "end": "d", "chunk": []any{msg("$2", 2000), msg("$1", 1000)}})
			}
			t.Fatalf("unexpected from %q", r.URL.Query().Get("from"))
		case strings.HasSuffix(path, "/joined_members"):
			return jsonResponse(t, r, http.StatusOK, map[string]any{"joined": map[string]any{"@me:example.com": map[string]any{"display_name": "Me"}}})
		case strings.HasPrefix(path, "/_matrix/client/unstable/im.nheko.summary/") || strings.Contains(path, "summary"):
			return jsonResponse(t, r, http.StatusNotFound, map[string]any{"errcode": "M_UNRECOGNIZED"})
		}
		t.Fatalf("unexpected %s %s", r.Method, path)
		return nil
	})

	result, err := svc.ExportTimeline(context.Background(), ExportRequest{RoomID: "!room:example.com", Since: time.UnixMilli(1500)})
	if err != nil {
		t.Fatalf("ExportTimeline() error = %v", err)
	}
	if pages != 3 || result.StopReason != ExportStopSince || !result.Complete || result.EventsScanned != 4 {
		t.Fatalf("pages = %d, result = %#v", pages, result)
	}
	var ids []string
	for _, msg := range result.Messages {
		ids = append(ids, msg.EventID)
	}
	if strings.Join(ids, ",") != "$2,$3,$4" {
		t.Fatalf("messages = %v, want oldest first and $1 excluded", ids)
	}

	pages = 0
	result, err = svc.ExportTimeline(context.Background(), ExportRequest{RoomID: "!room:example.com", MaxEvents: 2})
	if err != nil {
		t.Fatalf("ExportTimeline() error = %v", err)
	}
	if pages != 1 || result.StopReason != ExportStopMaxEvents || result.Complete || len(result.Messages) != 2 {
		t.Fatalf("pages = %d, result = %#v, want a stop at max_events", pages, result)
	}
}
