// Package transcript renders exported room history as a readable text
// transcript or JSON Lines, and keeps rendered exports behind short-lived
// capability links.
package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	matrixclient "github.com/ricelines/matrix-mcp/internal/matrix"
)

const (
	FormatText  = "text"
	FormatJSONL = "jsonl"
)

// snippetRunes is the length of the quoted text of a replied-to message.
const snippetRunes = 60

// Text renders an export as a plain-text transcript: a header that explains
// the notation, then one block per message under a line per day.
func Text(res matrixclient.ExportResult, loc *time.Location, generated time.Time) []byte {
	if loc == nil {
		loc = time.UTC
	}
	var b bytes.Buffer
	byID := make(map[string]*matrixclient.CompactMessage, len(res.Messages))
	for i := range res.Messages {
		byID[res.Messages[i].EventID] = &res.Messages[i]
	}

	room := res.RoomID
	if res.RoomName != "" {
		room = res.RoomName + " (" + res.RoomID + ")"
	}
	fmt.Fprintf(&b, "# Matrix room export: %s\n", room)
	fmt.Fprintf(&b, "# Generated %s. Times are %s (UTC%s).\n", generated.In(loc).Format("2006-01-02 15:04"), locationLabel(loc), generated.In(loc).Format("-07:00"))
	if len(res.Messages) > 0 {
		fmt.Fprintf(&b, "# Period: %s – %s\n", formatStamp(res.Messages[0].TimestampMS, loc), formatStamp(res.Messages[len(res.Messages)-1].TimestampMS, loc))
	}
	fmt.Fprintf(&b, "# %d messages from %d events. %s\n", len(res.Messages), res.EventsScanned, stopLine(res))
	if len(res.Senders) > 0 {
		fmt.Fprintf(&b, "# Senders: %s\n", sendersLine(res.Senders))
	}
	if len(res.Omitted) > 0 {
		fmt.Fprintf(&b, "# Left out: %s\n", omittedLine(res.Omitted))
	}
	b.WriteString("# Notation: (↩ time Name «text») = the message this one replies to; (edited) = the text is the last edit;\n")
	b.WriteString("#   [deleted] = a deleted message or a withdrawn reaction; [undecryptable: reason];\n")
	b.WriteString("#   attachments as [voice 0:31 $event_id] - fetch one with matrix.v1.media.download(room_id, event_id).\n")

	day := ""
	for i := range res.Messages {
		msg := &res.Messages[i]
		t := time.UnixMilli(msg.TimestampMS).In(loc)
		if d := t.Format("2006-01-02 Mon"); d != day {
			day = d
			fmt.Fprintf(&b, "\n== %s ==\n", day)
		}
		fmt.Fprintf(&b, "[%s] %s", t.Format("15:04"), msg.From)
		if msg.ReplyTo != "" {
			b.WriteString(" (↩ " + replyRef(msg.ReplyTo, byID, t, loc) + ")")
		}
		b.WriteString(": ")
		b.WriteString(indent(messageText(msg)))
		b.WriteString("\n")
		if len(msg.Reactions) > 0 {
			b.WriteString("    reactions: " + reactionsLine(msg.Reactions) + "\n")
		}
	}
	return b.Bytes()
}

// JSONL renders a header object followed by one compact message per line.
func JSONL(res matrixclient.ExportResult, generated time.Time) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	header := struct {
		matrixclient.ExportResult
		Generated string `json:"generated"`
		Messages  int    `json:"messages"`
	}{res, generated.UTC().Format(time.RFC3339), len(res.Messages)}
	if err := enc.Encode(header); err != nil {
		return nil, err
	}
	for _, msg := range res.Messages {
		if err := enc.Encode(msg); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

func locationLabel(loc *time.Location) string {
	if loc.String() == "" || loc.String() == "Local" {
		return "local time"
	}
	return loc.String()
}

func formatStamp(ms int64, loc *time.Location) string {
	return time.UnixMilli(ms).In(loc).Format("2006-01-02 15:04")
}

func stopLine(res matrixclient.ExportResult) string {
	switch res.StopReason {
	case matrixclient.ExportStopRoomStart:
		return "Complete: reached the start of the room."
	case matrixclient.ExportStopSince:
		return "Complete for the requested period."
	case matrixclient.ExportStopMaxEvents:
		return "INCOMPLETE: stopped at max_events, older history is not included."
	}
	return ""
}

func sendersLine(senders map[string][]string) string {
	names := make([]string, 0, len(senders))
	for name := range senders {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+" = "+strings.Join(senders[name], ", "))
	}
	return strings.Join(parts, "; ")
}

func omittedLine(omitted map[string]int) string {
	keys := make([]string, 0, len(omitted))
	for key := range omitted {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", key, omitted[key]))
	}
	return strings.Join(parts, ", ")
}

func reactionsLine(reactions map[string][]string) string {
	keys := make([]string, 0, len(reactions))
	for key := range reactions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+" "+strings.Join(reactions[key], ", "))
	}
	return strings.Join(parts, "; ")
}

// messageText is the body of one transcript entry, without sender and time.
func messageText(msg *matrixclient.CompactMessage) string {
	var parts []string
	switch msg.Kind {
	case "deleted":
		return "[deleted]"
	case "undecryptable":
		return "[undecryptable: " + msg.Error + "]"
	case "edit":
		parts = append(parts, "(edit of "+msg.EditOf+")")
	case "reaction":
		return "(reaction " + msg.Text + " to " + msg.ReactionTo + ")"
	case "notice", "emote", "location":
		parts = append(parts, "["+msg.Kind+"]")
	case "":
	default:
		if msg.Media == nil {
			parts = append(parts, "["+msg.Kind+"]")
		}
	}
	if msg.Media != nil {
		parts = append(parts, mediaLabel(msg))
	}
	if msg.Text != "" {
		parts = append(parts, msg.Text)
	}
	if msg.Edited {
		parts = append(parts, "(edited)")
	}
	return strings.Join(parts, " ")
}

func mediaLabel(msg *matrixclient.CompactMessage) string {
	kind := msg.Kind
	if kind == "edit" {
		kind = "media"
	}
	label := []string{kind}
	if msg.Media.FileName != "" {
		label = append(label, msg.Media.FileName)
	}
	if msg.Media.DurationMS > 0 {
		label = append(label, formatDuration(msg.Media.DurationMS))
	}
	if msg.Media.Size > 0 && msg.Media.DurationMS == 0 {
		label = append(label, formatSize(msg.Media.Size))
	}
	label = append(label, msg.EventID)
	text := "[" + strings.Join(label, " ") + "]"
	if msg.Media.URL != "" {
		text += " " + msg.Media.URL
	}
	return text
}

func formatDuration(ms int) string {
	seconds := (ms + 500) / 1000
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

func formatSize(size int) string {
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%d KB", size>>10)
	}
	return fmt.Sprintf("%d B", size)
}

// replyRef names the replied-to message: its time (with the date when it was
// on another day), sender and the start of its text; the event ID when the
// message is not in the export.
func replyRef(eventID string, byID map[string]*matrixclient.CompactMessage, at time.Time, loc *time.Location) string {
	target, ok := byID[eventID]
	if !ok {
		return eventID
	}
	tt := time.UnixMilli(target.TimestampMS).In(loc)
	stamp := tt.Format("15:04")
	if tt.Format("2006-01-02") != at.Format("2006-01-02") {
		stamp = tt.Format("02.01 15:04")
	}
	return stamp + " " + target.From + " «" + snippet(messageText(target)) + "»"
}

func snippet(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= snippetRunes {
		return text
	}
	return strings.TrimSpace(string(runes[:snippetRunes])) + "…"
}

// indent puts continuation lines of a multi-line message under its header.
func indent(text string) string {
	return strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\n    ")
}
