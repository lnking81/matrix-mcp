package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// compactEventTypes are the event types fetched in compact mode. Encrypted
// events are fetched too: their real type is only known after decryption.
var compactEventTypes = []event.Type{
	event.EventMessage,
	event.EventEncrypted,
	event.EventSticker,
	event.EventReaction,
}

// CompactMessage is one message reduced to what a reader needs: plain text
// (no HTML copy), edits and reactions folded in, media as metadata.
type CompactMessage struct {
	EventID    string              `json:"event_id"`
	Time       string              `json:"time" jsonschema:"RFC 3339 time in the requested time zone"`
	From       string              `json:"from" jsonschema:"Sender display name; see senders for the user IDs behind each name"`
	Kind       string              `json:"kind,omitempty" jsonschema:"Omitted for plain text. notice, emote, image, video, voice, audio, file, sticker, location, deleted (a deleted message or a withdrawn reaction), undecryptable, edit (an edit whose original is not in this batch), reaction (a reaction whose target is not in this batch), or another event type"`
	Text       string              `json:"text,omitempty" jsonschema:"Message text; for media the caption"`
	ReplyTo    string              `json:"reply_to,omitempty" jsonschema:"Event ID of the message this one replies to"`
	Thread     string              `json:"thread,omitempty" jsonschema:"Event ID of the thread root"`
	Edited     bool                `json:"edited,omitempty" jsonschema:"text is the latest edit"`
	EditOf     string              `json:"edit_of,omitempty"`
	ReactionTo string              `json:"reaction_to,omitempty"`
	Media      *CompactMedia       `json:"media,omitempty"`
	Reactions  map[string][]string `json:"reactions,omitempty" jsonschema:"Reaction key -> names of the people who reacted"`
	Error      string              `json:"error,omitempty" jsonschema:"Why the event could not be decrypted"`

	TimestampMS int64     `json:"-"`
	SenderID    id.UserID `json:"-"`
}

type CompactMedia struct {
	FileName   string `json:"filename,omitempty"`
	MIMEType   string `json:"mimetype,omitempty"`
	Size       int    `json:"size,omitempty"`
	DurationMS int    `json:"duration_ms,omitempty"`
	URL        string `json:"url,omitempty" jsonschema:"Short-lived download link, only when media links were requested"`
}

type CompactMessagesResult struct {
	Start    string              `json:"start,omitempty"`
	End      string              `json:"end,omitempty"`
	Timezone string              `json:"timezone"`
	Messages []CompactMessage    `json:"messages"`
	Senders  map[string][]string `json:"senders" jsonschema:"Display name -> user IDs (one person can have several bridged IDs)"`
	Omitted  map[string]int      `json:"omitted,omitempty" jsonschema:"Event types left out, with counts"`
}

// ListMessagesCompact pages a room timeline like ListMessages, but asks the
// homeserver for message events only and returns them in compact form.
func (s *Service) ListMessagesCompact(ctx context.Context, req ListMessagesRequest) (CompactMessagesResult, error) {
	direction, err := parseDirection(req.Direction)
	if err != nil {
		return CompactMessagesResult{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	filter := &mautrix.FilterPart{Types: compactEventTypes}
	resp, err := s.client.Messages(ctx, id.RoomID(req.RoomID), req.From, req.To, direction, filter, limit)
	if err != nil {
		return CompactMessagesResult{}, fmt.Errorf("list room messages: %w", err)
	}
	decoded := s.decodeEvents(ctx, req.RoomID, resp.Chunk, req.IncludeUndecryptable)
	names := s.senderNames(ctx, req.RoomID, decoded)
	messages, omitted := compactEvents(decoded, names, req.Location)
	return CompactMessagesResult{
		Start:    resp.Start,
		End:      resp.End,
		Timezone: locationName(req.Location),
		Messages: messages,
		Senders:  sendersByName(names),
		Omitted:  omitted,
	}, nil
}

func locationName(loc *time.Location) string {
	if loc == nil {
		return "UTC"
	}
	return loc.String()
}

// senderNames resolves display names for every sender in the batch: joined
// members first, then the user's profile, then the localpart.
func (s *Service) senderNames(ctx context.Context, roomID string, decoded []decodedEvent) map[id.UserID]string {
	names := make(map[id.UserID]string)
	for _, d := range decoded {
		names[d.evt.Sender] = ""
	}
	if len(names) == 0 {
		return names
	}
	if s.client != nil {
		if resp, err := s.client.JoinedMembers(ctx, id.RoomID(roomID)); err == nil {
			for userID, member := range resp.Joined {
				if _, ok := names[userID]; ok {
					names[userID] = strings.TrimSpace(member.DisplayName)
				}
			}
		}
	}
	for userID, name := range names {
		if name != "" {
			continue
		}
		if s.client != nil {
			if profile, err := s.client.GetProfile(ctx, userID); err == nil && strings.TrimSpace(profile.DisplayName) != "" {
				names[userID] = strings.TrimSpace(profile.DisplayName)
				continue
			}
		}
		names[userID] = userID.Localpart()
	}
	return names
}

func sendersByName(names map[id.UserID]string) map[string][]string {
	result := make(map[string][]string, len(names))
	for userID, name := range names {
		result[name] = append(result[name], userID.String())
	}
	for _, ids := range result {
		sort.Strings(ids)
	}
	return result
}

// messageContent is the part of m.room.message / m.sticker / m.reaction
// content that compact mode reads.
type messageContent struct {
	MsgType  string `json:"msgtype"`
	Body     string `json:"body"`
	FileName string `json:"filename"`
	GeoURI   string `json:"geo_uri"`
	Info     struct {
		MIMEType string `json:"mimetype"`
		Size     int    `json:"size"`
		Duration int    `json:"duration"`
	} `json:"info"`
	Voice      json.RawMessage `json:"org.matrix.msc3245.voice"`
	NewContent *messageContent `json:"m.new_content"`
	RelatesTo  struct {
		RelType   string `json:"rel_type"`
		EventID   string `json:"event_id"`
		Key       string `json:"key"`
		InReplyTo struct {
			EventID string `json:"event_id"`
		} `json:"m.in_reply_to"`
	} `json:"m.relates_to"`
}

func parseMessageContent(evt *event.Event) (messageContent, map[string]any) {
	raw := cloneContentMap(evt.Content)
	var content messageContent
	if data, err := json.Marshal(raw); err == nil {
		_ = json.Unmarshal(data, &content)
	}
	return content, raw
}

// compactEvents turns decoded events into compact messages in chronological
// order. Edits and reactions are folded into their target when the target is
// in the same batch; otherwise they are kept as their own entries.
func compactEvents(decoded []decodedEvent, names map[id.UserID]string, loc *time.Location) ([]CompactMessage, map[string]int) {
	if loc == nil {
		loc = time.UTC
	}
	ordered := make([]decodedEvent, len(decoded))
	copy(ordered, decoded)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].evt.Timestamp != ordered[j].evt.Timestamp {
			return ordered[i].evt.Timestamp < ordered[j].evt.Timestamp
		}
		return ordered[i].evt.ID < ordered[j].evt.ID
	})

	var out []*CompactMessage
	byID := make(map[string]*CompactMessage)
	omitted := make(map[string]int)
	newMessage := func(evt *event.Event) *CompactMessage {
		return &CompactMessage{
			EventID:     evt.ID.String(),
			Time:        time.UnixMilli(evt.Timestamp).In(loc).Format(time.RFC3339),
			From:        names[evt.Sender],
			TimestampMS: evt.Timestamp,
			SenderID:    evt.Sender,
		}
	}
	add := func(msg *CompactMessage) {
		out = append(out, msg)
		byID[msg.EventID] = msg
	}

	for _, d := range ordered {
		evt := d.evt
		switch {
		case d.decryptErr != nil:
			msg := newMessage(evt)
			msg.Kind = "undecryptable"
			msg.Error = undecryptableReason(evt, d.decryptErr)
			add(msg)
			continue
		case d.redacted:
			if evt.Type.Type == event.EventReaction.Type {
				omitted["m.reaction (withdrawn)"]++
				continue
			}
			msg := newMessage(evt)
			msg.Kind = "deleted"
			add(msg)
			continue
		}

		content, raw := parseMessageContent(evt)
		switch evt.Type.Type {
		case event.EventReaction.Type:
			if content.RelatesTo.RelType != string(event.RelAnnotation) || content.RelatesTo.Key == "" {
				omitted[evt.Type.Type]++
				continue
			}
			if target, ok := byID[content.RelatesTo.EventID]; ok {
				if target.Reactions == nil {
					target.Reactions = make(map[string][]string)
				}
				target.Reactions[content.RelatesTo.Key] = append(target.Reactions[content.RelatesTo.Key], names[evt.Sender])
				continue
			}
			msg := newMessage(evt)
			msg.Kind = "reaction"
			msg.Text = content.RelatesTo.Key
			msg.ReactionTo = content.RelatesTo.EventID
			add(msg)
		case event.EventMessage.Type, event.EventSticker.Type:
			if content.RelatesTo.RelType == string(event.RelReplace) && content.NewContent != nil {
				target, ok := byID[content.RelatesTo.EventID]
				// Only the original sender may edit a message.
				if ok && target.SenderID == evt.Sender {
					applyEdit(target, *content.NewContent)
					continue
				}
				msg := newMessage(evt)
				msg.EditOf = content.RelatesTo.EventID
				fillMessage(msg, evt.Type, *content.NewContent)
				msg.Kind = "edit"
				add(msg)
				continue
			}
			msg := newMessage(evt)
			fillMessage(msg, evt.Type, content)
			msg.ReplyTo = content.RelatesTo.InReplyTo.EventID
			if content.RelatesTo.RelType == string(event.RelThread) {
				msg.Thread = content.RelatesTo.EventID
			}
			if msg.ReplyTo != "" {
				msg.Text = stripReplyFallback(msg.Text)
			}
			add(msg)
		default:
			// Polls and other message-like events carry a fallback body.
			if body, ok := raw["body"].(string); ok && body != "" {
				msg := newMessage(evt)
				msg.Kind = evt.Type.Type
				msg.Text = body
				add(msg)
				continue
			}
			omitted[evt.Type.Type]++
		}
	}

	result := make([]CompactMessage, 0, len(out))
	for _, msg := range out {
		result = append(result, *msg)
	}
	if len(omitted) == 0 {
		omitted = nil
	}
	return result, omitted
}

func fillMessage(msg *CompactMessage, evtType event.Type, content messageContent) {
	msg.Text = content.Body
	switch {
	case evtType.Type == event.EventSticker.Type:
		msg.Kind = "sticker"
	case content.MsgType == "" || content.MsgType == string(event.MsgText):
		msg.Kind = ""
	case content.MsgType == string(event.MsgNotice):
		msg.Kind = "notice"
	case content.MsgType == string(event.MsgEmote):
		msg.Kind = "emote"
	case content.MsgType == string(event.MsgLocation):
		msg.Kind = "location"
		if content.GeoURI != "" {
			msg.Text = strings.TrimSpace(content.Body + " " + content.GeoURI)
		}
	case content.MsgType == string(event.MsgImage):
		msg.Kind = "image"
	case content.MsgType == string(event.MsgVideo):
		msg.Kind = "video"
	case content.MsgType == string(event.MsgAudio):
		msg.Kind = "audio"
		if len(content.Voice) > 0 {
			msg.Kind = "voice"
		}
	case content.MsgType == string(event.MsgFile):
		msg.Kind = "file"
	default:
		msg.Kind = content.MsgType
	}
	if isMediaKind(msg.Kind) {
		media := &CompactMedia{
			FileName:   content.FileName,
			MIMEType:   content.Info.MIMEType,
			Size:       content.Info.Size,
			DurationMS: content.Info.Duration,
		}
		// MSC2530: with a separate filename, body is the caption; without
		// one, body is the file name.
		if media.FileName == "" {
			media.FileName = content.Body
			msg.Text = ""
		} else if content.Body == media.FileName {
			msg.Text = ""
		}
		if msg.Kind == "voice" {
			media.FileName = ""
		}
		msg.Media = media
	}
}

func isMediaKind(kind string) bool {
	switch kind {
	case "image", "video", "audio", "voice", "file", "sticker":
		return true
	}
	return false
}

func applyEdit(target *CompactMessage, content messageContent) {
	target.Edited = true
	if isMediaKind(target.Kind) {
		// A media edit changes the caption only.
		if content.Body != "" && (target.Media == nil || content.Body != target.Media.FileName) {
			target.Text = content.Body
		}
		return
	}
	target.Text = content.Body
}

// stripReplyFallback removes the quoted "> <@user> ..." block that older
// clients put in front of a reply's body.
func stripReplyFallback(body string) string {
	if !strings.HasPrefix(body, "> ") {
		return body
	}
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) && strings.HasPrefix(lines[i], ">") {
		i++
	}
	if i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) {
		return body
	}
	return strings.Join(lines[i:], "\n")
}
