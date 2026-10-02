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
