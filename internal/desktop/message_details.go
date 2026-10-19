package desktop

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func replyStatsRows(s *store.MessageStats) [][2]string {
	rows := [][2]string{{"Input tokens", "—"}, {"Cached input tokens", "—"}, {"Output tokens", "—"}, {"Time to first token", "—"}, {"Duration", "—"}, {"Output speed", "—"}}
	if s == nil {
		return rows
	}
	if s.InputAvailable {
		rows[0][1] = fmt.Sprint(s.InputTokens)
	}
	if s.CachedInputAvailable {
		rows[1][1] = fmt.Sprint(s.CachedInputTokens)
	}
	if s.OutputAvailable {
		rows[2][1] = fmt.Sprint(s.OutputTokens)
	}
	if s.TimeToFirstTokenMS > 0 {
		rows[3][1] = fmt.Sprintf("%.1fs", float64(s.TimeToFirstTokenMS)/1000)
	}
	if s.DurationMS > 0 {
		rows[4][1] = fmt.Sprintf("%.1fs", float64(s.DurationMS)/1000)
	}
	// Match ChatWise: output tokens divided by the entire response duration,
	// including time to first token. Only use usage actually reported by the API.
	if s.OutputAvailable && s.DurationMS > 0 && s.OutputTokens > 0 {
		rows[5][1] = fmt.Sprintf("%.1f T/s", float64(s.OutputTokens)*1000/float64(s.DurationMS))
	}
	return rows
}

func accumulateReplyStats(s *store.MessageStats, completion provider.Completion, first bool, offset time.Duration) {
	u := completion.Usage
	s.InputTokens += u.InputTokens
	s.OutputTokens += u.OutputTokens
	s.CachedInputTokens += u.CachedInputTokens
	if first {
		s.InputAvailable, s.OutputAvailable, s.CachedInputAvailable = u.InputAvailable, u.OutputAvailable, u.CachedInputAvailable
	} else {
		s.InputAvailable = s.InputAvailable && u.InputAvailable
		s.OutputAvailable = s.OutputAvailable && u.OutputAvailable
		s.CachedInputAvailable = s.CachedInputAvailable && u.CachedInputAvailable
	}
	s.UsageAvailable = s.InputAvailable || s.OutputAvailable || s.CachedInputAvailable
	if s.TimeToFirstTokenMS == 0 && completion.Metrics.TimeToFirstToken > 0 {
		s.TimeToFirstTokenMS = max(1, (offset + completion.Metrics.TimeToFirstToken).Milliseconds())
	}
	// Track generation time separately from wall-clock response duration,
	// wait and time spent waiting for a tool or the user's tool approval.
	if completion.Metrics.TimeToFirstToken > 0 {
		s.GenerationMS += max(0, (completion.Metrics.Duration - completion.Metrics.TimeToFirstToken).Milliseconds())
	}
}

func messageStatsButton(c *ui.Context, m *store.Message) bool {
	b := messageActionButton(c, iResponseDetails, "Response details")
	open := ui.Local(b, "open", func() bool { return false })
	if b.Clicked() {
		*open = !*open
	}
	ui.Popover(c, b, open, func() {
		ui.Column(c).Width(280).Padding(8).Gap(8).Children(func() {
			if m.Model != "" {
				ui.Text(c, m.Model).FontSize(12).FontWeight(500).SingleLine()
			}
			for _, row := range replyStatsRows(m.Stats) {
				ui.Row(c).Gap(16).Children(func() {
					ui.Text(c, row[0]).FontSize(12).TextColor(c.Theme().TextMuted)
					ui.Spacer(c)
					ui.Text(c, row[1]).FontSize(14)
				})
			}
			if m.Stats == nil || !m.Stats.UsageAvailable {
				ui.Text(c, "Token usage was not reported for this response.").FontSize(11).TextColor(c.Theme().TextMuted)
			}
		})
	})
	return *open
}

func (a *app) reasoningView(c *ui.Context, m *store.Message, streaming bool) {
	if m.Reasoning == "" {
		return
	}
	box := ui.Column(c.Key("reasoning:" + m.ID)).FillWidth().Gap(6)
	expanded := ui.Local(box, "expanded", func() bool { return false })
	box.Children(func() {
		label := "Thinking"
		if !streaming {
			label = "Thought process"
		}
		button := ui.ButtonBase(c).Label("Toggle reasoning").AlignSelf(ui.Start).Justify(ui.Start).Padding(4, 0).Gap(5).TextColor(c.Theme().TextMuted)
		button.Children(func() {
			icon := ui.Icon(c, iChevron).Size(12, 12)
			if *expanded {
				icon.Rotate(90)
			}
			ui.Text(c, label).FontSize(13).FontWeight(500)
		})
		if button.Clicked() {
			*expanded = !*expanded
		}
		if *expanded {
			ui.Text(c, m.Reasoning).Selectable().FontSize(13).LineHeight(1.5).TextColor(c.Theme().TextMuted).Padding(6, 12).BorderWidth(0, 0, 0, 2).BorderColor(c.Theme().Border)
		}
	})
}
