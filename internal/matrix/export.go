package matrix

import (
	"context"
	"fmt"
	"log"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	exportPageSize         = 250
	DefaultExportMaxEvents = 20000
	MaxExportMaxEvents     = 100000
)

// Export stop reasons.
const (
	ExportStopRoomStart = "room start"
	ExportStopSince     = "since"
	ExportStopMaxEvents = "max_events"
)

type ExportRequest struct {
	RoomID string
	// Since and Until bound the export by event time; zero means unbounded.
	Since time.Time
	Until time.Time
	// MaxEvents caps how many events are read (DefaultExportMaxEvents when 0).
	MaxEvents            int
	IncludeUndecryptable bool
	Location             *time.Location
}

type ExportResult struct {
	RoomID        string              `json:"room_id"`
	RoomName      string              `json:"room_name,omitempty"`
	Timezone      string              `json:"timezone"`
	Messages      []CompactMessage    `json:"-"`
	Senders       map[string][]string `json:"senders"`
	Omitted       map[string]int      `json:"omitted,omitempty"`
	EventsScanned int                 `json:"events_scanned"`
	// Complete is true when the export reached the start of the room or the
	// requested since bound; false when it stopped at max_events.
	Complete   bool   `json:"complete"`
	StopReason string `json:"stop_reason"`
	Oldest     string `json:"oldest,omitempty"`
	Newest     string `json:"newest,omitempty"`
}

// ExportTimeline reads a room's message history backwards from the newest
// event, page by page, until it reaches the start of the room, the since
// bound or max_events, and returns it in compact form, oldest first. Edits
// and reactions are folded across the whole export.
func (s *Service) ExportTimeline(ctx context.Context, req ExportRequest) (ExportResult, error) {
	maxEvents := req.MaxEvents
	if maxEvents <= 0 {
		maxEvents = DefaultExportMaxEvents
	}
	if maxEvents > MaxExportMaxEvents {
		maxEvents = MaxExportMaxEvents
	}
	roomID := id.RoomID(req.RoomID)
	filter := &mautrix.FilterPart{Types: compactEventTypes}

	var collected []*event.Event
	scanned := 0
	from := ""
	stop := ""
	started := time.Now()
	for stop == "" {
		pageSize := exportPageSize
		if left := maxEvents - scanned; left < pageSize {
			pageSize = left
		}
		resp, err := s.client.Messages(ctx, roomID, from, "", mautrix.DirectionBackward, filter, pageSize)
		if err != nil {
			return ExportResult{}, fmt.Errorf("list room messages (after %d events): %w", scanned, err)
		}
		for _, evt := range resp.Chunk {
			if evt == nil {
				continue
			}
			scanned++
			eventTime := time.UnixMilli(evt.Timestamp)
			if !req.Since.IsZero() && eventTime.Before(req.Since) {
				stop = ExportStopSince
				break
			}
			if req.Until.IsZero() || !eventTime.After(req.Until) {
				collected = append(collected, evt)
			}
		}
		switch {
		case stop != "":
		case resp.End == "" || resp.End == from:
			// An empty chunk alone does not mean the start: filtering can
			// empty a page; only a missing end token does.
			stop = ExportStopRoomStart
		case scanned >= maxEvents:
			stop = ExportStopMaxEvents
		default:
			from = resp.End
		}
	}

	decoded := s.decodeEvents(ctx, req.RoomID, collected, req.IncludeUndecryptable)
	names := s.senderNames(ctx, req.RoomID, decoded)
	messages, omitted := compactEvents(decoded, names, req.Location)

	result := ExportResult{
		RoomID:        req.RoomID,
		Timezone:      locationName(req.Location),
		Messages:      messages,
		Senders:       sendersByName(names),
		Omitted:       omitted,
		EventsScanned: scanned,
		Complete:      stop != ExportStopMaxEvents,
		StopReason:    stop,
	}
	if room, err := s.GetRoom(ctx, req.RoomID); err == nil {
		result.RoomName = room.DisplayName
	}
	if len(messages) > 0 {
		result.Oldest = messages[0].Time
		result.Newest = messages[len(messages)-1].Time
	}
	log.Printf("timeline: export %s: %d events scanned, %d messages, stopped at %s, %s",
		req.RoomID, scanned, len(messages), stop, time.Since(started).Round(time.Millisecond))
	return result, nil
}
