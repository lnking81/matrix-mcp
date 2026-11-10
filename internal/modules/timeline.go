package modules

import (
	"context"
	_ "time/tzdata" // time zones for compact/export times; the distroless image has no zoneinfo

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ricelines/matrix-mcp/internal/catalog"
	matrixclient "github.com/ricelines/matrix-mcp/internal/matrix"
	"github.com/ricelines/matrix-mcp/internal/scopes"
)

type timelineMessagesInput struct {
	RoomID    string `json:"room_id" jsonschema:"Matrix room ID whose timeline should be paginated"`
	From      string `json:"from,omitempty" jsonschema:"Pagination token to continue from"`
	To        string `json:"to,omitempty" jsonschema:"Pagination token bound for the request"`
	Direction string `json:"direction,omitempty" jsonschema:"Timeline direction: 'b' for backward or 'f' for forward. Defaults to 'b'."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum number of timeline events to return"`
	Compact   bool   `json:"compact,omitempty" jsonschema:"Return messages in compact form (plain text only, edits and reactions folded in, sender names, local times) instead of raw events. Asks the homeserver for message events only, so state events are not returned. Recommended for reading a conversation."`
	Timezone  string `json:"timezone,omitempty" jsonschema:"IANA time zone for compact message times, e.g. Asia/Makassar. Defaults to UTC."`
	// IncludeUndecryptable defaults to false, which keeps upstream behavior
	// (undecryptable events are skipped).
	IncludeUndecryptable bool `json:"include_undecryptable,omitempty" jsonschema:"Return encrypted events that cannot be decrypted as placeholders with the reason, instead of skipping them"`
}

type timelineExportInput struct {
	RoomID               string `json:"room_id" jsonschema:"Matrix room ID to export"`
	Since                string `json:"since,omitempty" jsonschema:"Oldest time to include: RFC 3339, or YYYY-MM-DD / YYYY-MM-DD HH:MM in timezone. Default: the start of the room."`
	Until                string `json:"until,omitempty" jsonschema:"Newest time to include, same formats. Default: now."`
	Timezone             string `json:"timezone,omitempty" jsonschema:"IANA time zone for times in the transcript and for since/until without an offset, e.g. Asia/Makassar. Defaults to UTC."`
	Format               string `json:"format,omitempty" jsonschema:"text (default): a readable transcript; jsonl: a header line, then one compact message per line"`
	MaxEvents            int    `json:"max_events,omitempty" jsonschema:"Stop after reading this many events (default 20000, max 100000); the result says when it stopped early"`
	MediaLinks           bool   `json:"media_links,omitempty" jsonschema:"Add a short-lived download link (30 min) to every attachment"`
	Inline               bool   `json:"inline,omitempty" jsonschema:"Also return the transcript itself in this result when it is at most 256 KB; for clients that cannot download the link. Narrow since/until for long chats."`
	ExcludeUndecryptable bool   `json:"exclude_undecryptable,omitempty" jsonschema:"Leave out encrypted events that cannot be decrypted instead of listing them with the reason"`
}

type timelineEventInput struct {
	RoomID  string `json:"room_id" jsonschema:"Matrix room ID containing the event"`
	EventID string `json:"event_id" jsonschema:"Matrix event ID to fetch"`
}

type timelineContextInput struct {
	RoomID  string `json:"room_id" jsonschema:"Matrix room ID containing the event"`
	EventID string `json:"event_id" jsonschema:"Matrix event ID whose surrounding context should be fetched"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Maximum number of events before and after the target event"`
}

type timelineRelationsInput struct {
	RoomID       string `json:"room_id" jsonschema:"Matrix room ID containing the event"`
	EventID      string `json:"event_id" jsonschema:"Matrix event ID whose relations should be listed"`
	RelationType string `json:"relation_type,omitempty" jsonschema:"Optional Matrix relation type such as m.annotation, m.replace, or m.thread"`
	EventType    string `json:"event_type,omitempty" jsonschema:"Optional Matrix event type filter such as m.room.message or m.reaction"`
	Direction    string `json:"direction,omitempty" jsonschema:"Timeline direction: 'b' for backward or 'f' for forward. Defaults to 'b'."`
	From         string `json:"from,omitempty" jsonschema:"Pagination token to continue from"`
	To           string `json:"to,omitempty" jsonschema:"Pagination token bound for the request"`
	Limit        int    `json:"limit,omitempty" jsonschema:"Maximum number of relations to return"`
	Recurse      bool   `json:"recurse,omitempty" jsonschema:"Whether to recursively include transitive relations when the homeserver supports it"`
}

type timelineMessagesOutput struct {
	BaseResult
	Start  string                      `json:"start,omitempty"`
	End    string                      `json:"end,omitempty"`
	Events []matrixclient.EventSummary `json:"events"`
	State  []matrixclient.EventSummary `json:"state,omitempty"`
	// Compact mode fields.
	Timezone string                        `json:"timezone,omitempty"`
	Messages []matrixclient.CompactMessage `json:"messages,omitempty"`
	Senders  map[string][]string           `json:"senders,omitempty" jsonschema:"Display name -> user IDs (one person can have several bridged IDs)"`
	Omitted  map[string]int                `json:"omitted,omitempty" jsonschema:"Event types left out, with counts"`
}

type timelineExportOutput struct {
	BaseResult
	matrixclient.ExportResult
	Format       string `json:"format"`
	MessageCount int    `json:"messages"`
	Bytes        int    `json:"bytes"`
	DownloadURL  string `json:"download_url,omitempty" jsonschema:"Short-lived link to the transcript; works without the MCP auth header (curl, browser) until expires_at"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Inlined      bool   `json:"inlined" jsonschema:"Whether the transcript itself follows in this result"`
	Note         string `json:"note,omitempty"`
}

type timelineEventOutput struct {
	BaseResult
	Event matrixclient.EventSummary `json:"event"`
}

type timelineContextOutput struct {
	BaseResult
	Event        matrixclient.EventSummary   `json:"event"`
	EventsBefore []matrixclient.EventSummary `json:"events_before"`
	EventsAfter  []matrixclient.EventSummary `json:"events_after"`
	State        []matrixclient.EventSummary `json:"state,omitempty"`
	Start        string                      `json:"start,omitempty"`
	End          string                      `json:"end,omitempty"`
}

type timelineRelationsOutput struct {
	BaseResult
	Events         []matrixclient.EventSummary `json:"events"`
	NextBatch      string                      `json:"next_batch,omitempty"`
	PrevBatch      string                      `json:"prev_batch,omitempty"`
	RecursionDepth int                         `json:"recursion_depth,omitempty"`
}

func RegisterTimeline(r *catalog.Registrar, deps Dependencies, active scopes.Set) {
	r.AddModule("timeline", "Timeline pagination, event lookup, context, and relation inspection.")
	if !active.Allows(scopes.ScopeTimelineRead) {
		return
	}

	catalog.AddTool(r, "timeline", scopes.ScopeTimelineRead, &mcp.Tool{
		Name:        "matrix.v1.timeline.messages.list",
		Description: "Paginate the timeline of a room, returning timeline events and any supporting state included by the homeserver.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input timelineMessagesInput) (*mcp.CallToolResult, timelineMessagesOutput, error) {
		if err := requireNonEmpty("room_id", input.RoomID); err != nil {
			return nil, timelineMessagesOutput{}, err
		}
		loc, err := parseTimezone(input.Timezone)
		if err != nil {
			return nil, timelineMessagesOutput{}, err
		}
		request := matrixclient.ListMessagesRequest{
			RoomID:               input.RoomID,
			From:                 input.From,
			To:                   input.To,
			Direction:            input.Direction,
			Limit:                input.Limit,
			IncludeUndecryptable: input.IncludeUndecryptable,
			Location:             loc,
		}
		if input.Compact {
			compact, err := deps.Matrix.ListMessagesCompact(ctx, request)
			if err != nil {
				return nil, timelineMessagesOutput{}, err
			}
			messages := compact.Messages
			if messages == nil {
				messages = []matrixclient.CompactMessage{}
			}
			return nil, timelineMessagesOutput{
				BaseResult: deps.baseResult(),
				Start:      compact.Start,
				End:        compact.End,
				Events:     []matrixclient.EventSummary{},
				Timezone:   compact.Timezone,
				Messages:   messages,
				Senders:    compact.Senders,
				Omitted:    compact.Omitted,
			}, nil
		}
		result, err := deps.Matrix.ListMessages(ctx, request)
		if err != nil {
			return nil, timelineMessagesOutput{}, err
		}
		return nil, timelineMessagesOutput{
			BaseResult: deps.baseResult(),
			Start:      result.Start,
			End:        result.End,
			Events:     result.Events,
			State:      result.State,
		}, nil
	})

	catalog.AddTool(r, "timeline", scopes.ScopeTimelineRead, &mcp.Tool{
		Name: "matrix.v1.timeline.export",
		Description: "Export a room's message history (the whole room, or since/until) in one call: the server pages through it, " +
			"decrypts, folds edits and reactions, and renders a compact transcript. Returns stats and a short-lived download_url " +
			"(no auth header needed: curl it to a file, then read the file). Use this instead of paging timeline.messages.list " +
			"to read long conversations. Attachments are listed with their event IDs for matrix.v1.media.download.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input timelineExportInput) (*mcp.CallToolResult, timelineExportOutput, error) {
		return exportTimeline(ctx, deps, input)
	})

	catalog.AddTool(r, "timeline", scopes.ScopeTimelineRead, &mcp.Tool{
		Name:        "matrix.v1.timeline.event.get",
		Description: "Fetch one event from a room by event ID.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input timelineEventInput) (*mcp.CallToolResult, timelineEventOutput, error) {
		if err := requireNonEmpty("room_id", input.RoomID); err != nil {
			return nil, timelineEventOutput{}, err
		}
		if err := requireNonEmpty("event_id", input.EventID); err != nil {
			return nil, timelineEventOutput{}, err
		}
		evt, err := deps.Matrix.GetEvent(ctx, input.RoomID, input.EventID)
		if err != nil {
			return nil, timelineEventOutput{}, err
		}
		return nil, timelineEventOutput{BaseResult: deps.baseResult(), Event: evt}, nil
	})

	catalog.AddTool(r, "timeline", scopes.ScopeTimelineRead, &mcp.Tool{
		Name:        "matrix.v1.timeline.event.context.get",
		Description: "Fetch a target event together with surrounding timeline context and supporting state.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input timelineContextInput) (*mcp.CallToolResult, timelineContextOutput, error) {
		if err := requireNonEmpty("room_id", input.RoomID); err != nil {
			return nil, timelineContextOutput{}, err
		}
		if err := requireNonEmpty("event_id", input.EventID); err != nil {
			return nil, timelineContextOutput{}, err
		}
		contextResult, err := deps.Matrix.GetEventContext(ctx, input.RoomID, input.EventID, input.Limit)
		if err != nil {
			return nil, timelineContextOutput{}, err
		}
		return nil, timelineContextOutput{
			BaseResult:   deps.baseResult(),
			Event:        contextResult.Event,
			EventsBefore: contextResult.EventsBefore,
			EventsAfter:  contextResult.EventsAfter,
			State:        contextResult.State,
			Start:        contextResult.Start,
			End:          contextResult.End,
		}, nil
	})

	catalog.AddTool(r, "timeline", scopes.ScopeTimelineRead, &mcp.Tool{
		Name:        "matrix.v1.timeline.relations.list",
		Description: "List relation events connected to a target event, such as reactions or edits.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input timelineRelationsInput) (*mcp.CallToolResult, timelineRelationsOutput, error) {
		if err := requireNonEmpty("room_id", input.RoomID); err != nil {
			return nil, timelineRelationsOutput{}, err
		}
		if err := requireNonEmpty("event_id", input.EventID); err != nil {
			return nil, timelineRelationsOutput{}, err
		}
		result, err := deps.Matrix.ListRelations(ctx, matrixclient.ListRelationsRequest{
			RoomID:       input.RoomID,
			EventID:      input.EventID,
			RelationType: input.RelationType,
			EventType:    input.EventType,
			Direction:    input.Direction,
			From:         input.From,
			To:           input.To,
			Limit:        input.Limit,
			Recurse:      input.Recurse,
		})
		if err != nil {
			return nil, timelineRelationsOutput{}, err
		}
		return nil, timelineRelationsOutput{
			BaseResult:     deps.baseResult(),
			Events:         result.Events,
			NextBatch:      result.NextBatch,
			PrevBatch:      result.PrevBatch,
			RecursionDepth: result.RecursionDepth,
		}, nil
	})
}
