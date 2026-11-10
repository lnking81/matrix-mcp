package mcpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	matrixclient "github.com/ricelines/matrix-mcp/internal/matrix"
	"github.com/ricelines/matrix-mcp/internal/scopes"
)

func sampleExportBackend() *fakeMatrix {
	return &fakeMatrix{export: &matrixclient.ExportResult{
		RoomID:        "!room:example.com",
		RoomName:      "Алексей Дорош (WA)",
		Timezone:      "Asia/Makassar",
		EventsScanned: 3,
		Complete:      true,
		StopReason:    matrixclient.ExportStopRoomStart,
		Senders:       map[string][]string{"Алексей": {"@ghost:example.com"}},
		Messages: []matrixclient.CompactMessage{
			{EventID: "$1", From: "Алексей", Text: "привет", Time: "2026-11-09T15:58:00+08:00", TimestampMS: 1794211080000},
			{EventID: "$2", From: "Алексей", Kind: "voice", Time: "2026-11-09T15:59:00+08:00", TimestampMS: 1794211140000,
				Media: &matrixclient.CompactMedia{MIMEType: "audio/ogg", DurationMS: 4000}},
		},
	}}
}

func callToolOK(t *testing.T, server *Server, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	session := connectTestSession(t, server)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s error = %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned an error result: %#v", name, res.Content[0])
	}
	return res
}

func TestTimelineExportIssuesWorkingLink(t *testing.T) {
	backend := sampleExportBackend()
	server := NewWithOptions(backend, scopes.Default(), Options{PublicURL: "https://mcp.example.com"})

	res := callToolOK(t, server, "matrix.v1.timeline.export", map[string]any{
		"room_id": "!room:example.com", "timezone": "Asia/Makassar", "since": "2026-11-01", "media_links": true,
	})
	out := structuredMap(t, res)
	link, _ := out["download_url"].(string)
	if !strings.HasPrefix(link, "https://mcp.example.com/export/") || out["messages"] != float64(2) || out["complete"] != true || out["inlined"] != false {
		t.Fatalf("unexpected output %#v", out)
	}
	if got := backend.lastExport; got.Since.Format("2006-01-02T15:04:05-07:00") != "2026-11-01T00:00:00+08:00" || !got.IncludeUndecryptable || got.Location.String() != "Asia/Makassar" {
		t.Fatalf("export request = %#v", got)
	}

	routes := AccessLog(server.Routes("s3cret"))
	logs := captureLog(t)
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, strings.TrimPrefix(link, "https://mcp.example.com"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET export status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	for _, want := range []string{"# Matrix room export: Алексей Дорош (WA)", "[15:58] Алексей: привет", "[15:59] Алексей: [voice 0:04 $2] https://mcp.example.com/media/"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("export lacks %q:\n%s", want, body)
		}
	}
	token := strings.TrimPrefix(link, "https://mcp.example.com/export/")
	if strings.Contains(logs.String(), token) || !strings.Contains(logs.String(), "/export/<token>") {
		t.Fatalf("export token not redacted in the access log: %s", logs)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") || !strings.Contains(got, ".txt") {
		t.Fatalf("Content-Disposition = %q", got)
	}

	rec = httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/export/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown export token = %d, want 404", rec.Code)
	}
}

func TestTimelineExportInline(t *testing.T) {
	server := NewWithOptions(sampleExportBackend(), scopes.Default(), Options{})
	res := callToolOK(t, server, "matrix.v1.timeline.export", map[string]any{"room_id": "!room:example.com", "inline": true, "format": "jsonl"})
	if out := structuredMap(t, res); out["inlined"] != true || out["download_url"] != nil {
		t.Fatalf("unexpected output %#v", out)
	}
	if len(res.Content) != 2 {
		t.Fatalf("content blocks = %d, want summary + transcript", len(res.Content))
	}
	if text := res.Content[1].(*mcp.TextContent).Text; !strings.Contains(text, `"event_id":"$1"`) {
		t.Fatalf("inline transcript = %q", text)
	}
}

func TestTimelineExportWithoutPublicURLNeedsInline(t *testing.T) {
	server := NewWithOptions(sampleExportBackend(), scopes.Default(), Options{})
	session := connectTestSession(t, server)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "matrix.v1.timeline.export", Arguments: map[string]any{"room_id": "!room:example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("export without a public URL and without inline should fail")
	}
}

func TestTimelineMessagesCompact(t *testing.T) {
	backend := &fakeMatrix{compactMessages: &matrixclient.CompactMessagesResult{
		End:      "next",
		Timezone: "Asia/Makassar",
		Messages: []matrixclient.CompactMessage{{EventID: "$1", From: "Алексей", Text: "привет", Time: "2026-11-09T15:58:00+08:00"}},
		Senders:  map[string][]string{"Алексей": {"@ghost:example.com"}},
	}}
	server := New(backend, scopes.Default())
	res := callToolOK(t, server, "matrix.v1.timeline.messages.list", map[string]any{
		"room_id": "!room:example.com", "compact": true, "timezone": "Asia/Makassar", "include_undecryptable": true, "limit": 50,
	})
	out := structuredMap(t, res)
	messages, _ := out["messages"].([]any)
	if len(messages) != 1 || out["end"] != "next" || out["timezone"] != "Asia/Makassar" {
		t.Fatalf("unexpected output %#v", out)
	}
	if got := backend.lastMessages; !got.IncludeUndecryptable || got.Limit != 50 || got.Location.String() != "Asia/Makassar" {
		t.Fatalf("request = %#v", got)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; strings.Contains(text, "formatted_body") {
		t.Fatalf("compact output carries raw content: %s", text)
	}
}

func TestTimelineMessagesRejectsUnknownTimezone(t *testing.T) {
	server := New(&fakeMatrix{}, scopes.Default())
	session := connectTestSession(t, server)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "matrix.v1.timeline.messages.list", Arguments: map[string]any{"room_id": "!room:example.com", "compact": true, "timezone": "Mars/Base"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("unknown timezone accepted")
	}
}
