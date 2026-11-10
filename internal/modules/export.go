package modules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	matrixclient "github.com/ricelines/matrix-mcp/internal/matrix"
	"github.com/ricelines/matrix-mcp/internal/medialink"
	"github.com/ricelines/matrix-mcp/internal/transcript"
)

// maxInlineExportBytes bounds a transcript returned inside the tool result.
const maxInlineExportBytes = 256 << 10

func exportTimeline(ctx context.Context, deps Dependencies, input timelineExportInput) (*mcp.CallToolResult, timelineExportOutput, error) {
	if err := requireNonEmpty("room_id", input.RoomID); err != nil {
		return nil, timelineExportOutput{}, err
	}
	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format == "" {
		format = transcript.FormatText
	}
	if format != transcript.FormatText && format != transcript.FormatJSONL {
		return nil, timelineExportOutput{}, fmt.Errorf("format must be %q or %q", transcript.FormatText, transcript.FormatJSONL)
	}
	loc, err := parseTimezone(input.Timezone)
	if err != nil {
		return nil, timelineExportOutput{}, err
	}
	since, err := parseTimeBound("since", input.Since, loc)
	if err != nil {
		return nil, timelineExportOutput{}, err
	}
	until, err := parseTimeBound("until", input.Until, loc)
	if err != nil {
		return nil, timelineExportOutput{}, err
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		return nil, timelineExportOutput{}, errors.New("until is before since")
	}
	canLink := deps.Exports != nil && deps.PublicURL != ""
	if !canLink && !input.Inline {
		return nil, timelineExportOutput{}, errors.New("export links are disabled: the server has no public URL configured (MATRIX_MCP_PUBLIC_URL); pass inline=true for a short period")
	}

	res, err := deps.Matrix.ExportTimeline(ctx, matrixclient.ExportRequest{
		RoomID:               input.RoomID,
		Since:                since,
		Until:                until,
		MaxEvents:            input.MaxEvents,
		IncludeUndecryptable: !input.ExcludeUndecryptable,
		Location:             loc,
	})
	if err != nil {
		return nil, timelineExportOutput{}, err
	}

	if input.MediaLinks && deps.MediaLinks != nil && deps.PublicURL != "" {
		for i := range res.Messages {
			if res.Messages[i].Media == nil {
				continue
			}
			token, _, err := deps.MediaLinks.Create(medialink.Target{RoomID: input.RoomID, EventID: res.Messages[i].EventID})
			if err != nil {
				return nil, timelineExportOutput{}, err
			}
			res.Messages[i].Media.URL = deps.PublicURL + "/media/" + token
		}
	}

	now := time.Now()
	if deps.Now != nil {
		now = deps.Now()
	}
	var data []byte
	contentType := "text/plain; charset=utf-8"
	ext := ".txt"
	if format == transcript.FormatJSONL {
		data, err = transcript.JSONL(res, now)
		if err != nil {
			return nil, timelineExportOutput{}, err
		}
		contentType = "application/x-ndjson; charset=utf-8"
		ext = ".jsonl"
	} else {
		data = transcript.Text(res, loc, now)
	}

	out := timelineExportOutput{
		BaseResult:   deps.baseResult(),
		ExportResult: res,
		Format:       format,
		MessageCount: len(res.Messages),
		Bytes:        len(data),
	}
	if canLink {
		token, expires, err := deps.Exports.Put(transcript.File{
			Name:        exportFileName(res, now.In(loc), ext),
			ContentType: contentType,
			Data:        data,
		})
		if errors.Is(err, transcript.ErrTooLarge) {
			return nil, timelineExportOutput{}, fmt.Errorf("export is %d bytes, above the server limit: narrow since/until", len(data))
		}
		if err != nil {
			return nil, timelineExportOutput{}, err
		}
		out.DownloadURL = deps.PublicURL + "/export/" + token
		out.ExpiresAt = expires.UTC().Format(time.RFC3339)
		out.Note = fmt.Sprintf("The transcript is not in this result: download it, e.g. curl -sS -o %q '%s', then read the file. Give the link to the user if you cannot fetch it.",
			exportFileName(res, now.In(loc), ext), out.DownloadURL)
	}

	if !input.Inline {
		return nil, out, nil
	}
	if len(data) > maxInlineExportBytes {
		out.Note = strings.TrimSpace(fmt.Sprintf("Not inlined: %d bytes is above %d; narrow since/until. %s", len(data), maxInlineExportBytes, out.Note))
		return nil, out, nil
	}
	out.Inlined = true
	out.Note = ""
	text, err := json.Marshal(out)
	if err != nil {
		return nil, timelineExportOutput{}, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: string(text)},
		&mcp.TextContent{Text: string(data)},
	}}, out, nil
}

// parseTimezone loads an IANA zone; empty means UTC.
func parseTimezone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q (use an IANA name such as Asia/Makassar)", name)
	}
	return loc, nil
}

// parseTimeBound accepts RFC 3339, or a date or date and time in loc.
func parseTimeBound(field, value string, loc *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%s: cannot parse %q (use RFC 3339 or YYYY-MM-DD [HH:MM])", field, value)
}

func exportFileName(res matrixclient.ExportResult, at time.Time, ext string) string {
	name := strings.TrimSpace(res.RoomName)
	if name == "" {
		name = strings.TrimPrefix(strings.SplitN(res.RoomID, ":", 2)[0], "!")
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == 0x7f, strings.ContainsRune(`/\:*?"<>|`, r):
			return -1
		case r == ' ':
			return '_'
		}
		return r
	}, name)
	if name == "" {
		name = "room"
	}
	return name + "-" + at.Format("20060102-1504") + ext
}
