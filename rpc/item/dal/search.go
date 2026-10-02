package dal

import (
	"context"
	"time"

	search "eigenflux_server/kitex_gen/eigenflux/recordsearch"
	"eigenflux_server/pkg/recordsearch"

	"gorm.io/gorm"
)

func SearchOwnedBroadcasts(ctx context.Context, db *gorm.DB, req *search.SearchReq) (*search.SearchResp, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	owner, text := req.OwnerAgentId, req.Query
	pattern, id := recordsearch.Literal(text)
	sql := `SELECT r.item_id AS key, r.item_id AS id, LEFT(r.raw_content,100) AS title,
   r.raw_content || E'\n' || COALESCE(r.raw_notes,'') || E'\n' || COALESCE(p.summary,'') || E'\n' || COALESCE(p.summary_zh,'') AS body,
   CASE p.status WHEN 0 THEN 'pending' WHEN 1 THEN 'processing' WHEN 2 THEN 'failed' WHEN 3 THEN 'published' WHEN 4 THEN 'discarded' WHEN 5 THEN 'retracted' END AS status,
   p.updated_at, 0 AS conversation_id, 0 AS peer_id, '' AS short_id
   FROM raw_items r JOIN processed_items p ON p.item_id=r.item_id WHERE r.author_agent_id=? AND
   (r.item_id=? OR r.raw_content ILIKE ? ESCAPE '!' OR r.raw_notes ILIKE ? ESCAPE '!' OR p.summary ILIKE ? ESCAPE '!' OR p.summary_zh ILIKE ? ESCAPE '!')`
	args := []interface{}{owner, id, pattern, pattern, pattern, pattern}
	sql = `SELECT * FROM (` + sql + `) matches WHERE (CAST(? AS BIGINT)=0 OR key < ?) AND (?='' OR status=?) ORDER BY key DESC LIMIT ?`
	args = append(args, req.Cursor, req.Cursor, req.Status, req.Status, int(req.Limit)+1)
	var rows []recordsearch.Row
	if err := db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return recordsearch.Page(rows, text, int(req.Limit)), nil
}

// FilterSearchVisibility rechecks permissions even for cached/in-flight pages.
// This also covers writes through other entry points, without a stale ACL TTL.
func FilterSearchVisibility(ctx context.Context, db *gorm.DB, owner int64, kind string, page *search.SearchResp) error {
	if len(page.Items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(page.Items))
	for _, hit := range page.Items {
		ids = append(ids, hit.Id)
	}
	var visible []int64

	if err := db.WithContext(ctx).Raw(`SELECT r.item_id FROM raw_items r JOIN processed_items p ON p.item_id=r.item_id WHERE r.item_id IN ? AND r.author_agent_id=?`, ids, owner).Scan(&visible).Error; err != nil {
		return err
	}

	allowed := make(map[int64]bool, len(visible))
	for _, id := range visible {
		allowed[id] = true
	}
	kept := page.Items[:0]
	for _, hit := range page.Items {
		if allowed[hit.Id] {
			kept = append(kept, hit)
		}
	}
	page.Items = kept
	return nil
}
