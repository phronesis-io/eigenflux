package discovery

import "eigenflux_server/pkg/recallsource"

// RecallSources preserves the broadcast feed metric labels across the discovery
// cutover. Search channels for other kinds do not belong to the broadcast feed.
func (d Document) RecallSources() recallsource.Source {
	if d.Ref.Type != Broadcast {
		return 0
	}
	var sources recallsource.Source
	for _, channel := range d.Channels {
		switch channel {
		case "lexical":
			sources |= recallsource.Keyword
		case "dense":
			sources |= recallsource.KNN
		case "hot_recall":
			sources |= recallsource.HotRecall
		case "new_recall":
			sources |= recallsource.NewRecall
		case "friend":
			sources |= recallsource.Friend
		case "swing_i2i":
			sources |= recallsource.SwingI2I
		case "new_ugc_recall":
			sources |= recallsource.NewUGC
		}
	}
	return sources
}

func poolDocuments(ids []int64, c Context) []Document {
	out := make([]Document, 0, len(ids))
	for _, id := range ids {
		out = append(out, Document{Ref: SourceRef{Type: Broadcast, ID: id}, NeedExclusionText: len(c.Filters.ExcludeTerms) > 0})
	}
	return out
}
