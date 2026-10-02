package dal

import (
	"context"
	"time"

	search "eigenflux_server/kitex_gen/eigenflux/recordsearch"
	"eigenflux_server/pkg/recordsearch"

	"gorm.io/gorm"
)

func SearchMessages(ctx context.Context, db *gorm.DB, req *search.SearchReq) (*search.SearchResp, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	owner, text := req.OwnerAgentId, req.Query
	pattern, id := recordsearch.Literal(text)
	sql := `SELECT m.msg_id AS key, m.msg_id AS id, COALESCE(NULLIF(r.remark,''),a.agent_name,'') AS title,
   m.content || E'\n' || COALESCE(a.agent_name,'') || E'\n' || COALESCE(a.agent_name_en,'') || E'\n' || COALESCE(r.remark,'') AS body,
   CASE c.topic_status WHEN 0 THEN 'pending_verify' WHEN 2 THEN 'closed' ELSE 'open' END AS status,
   m.created_at AS updated_at, c.conv_id AS conversation_id, a.agent_id AS peer_id, a.short_id AS short_id
   FROM conversations c JOIN private_messages m ON m.conv_id=c.conv_id
   JOIN agents a ON a.agent_id=CASE WHEN c.participant_a=? THEN c.participant_b ELSE c.participant_a END
   LEFT JOIN user_relations r ON r.from_uid=? AND r.to_uid=a.agent_id AND r.rel_type=1
   WHERE c.status=0 AND (c.participant_a=? OR c.participant_b=?) AND
   (m.msg_id=? OR c.conv_id=? OR a.agent_id=? OR a.short_id=? OR m.content ILIKE ? ESCAPE '!' OR a.agent_name ILIKE ? ESCAPE '!' OR a.agent_name_en ILIKE ? ESCAPE '!' OR r.remark ILIKE ? ESCAPE '!')`
	args := []interface{}{owner, owner, owner, owner, id, id, id, text, pattern, pattern, pattern, pattern}
	sql = `SELECT * FROM (` + sql + `) matches WHERE (CAST(? AS BIGINT)=0 OR key < ?) AND (?='' OR status=?) ORDER BY key DESC LIMIT ?`
	args = append(args, req.Cursor, req.Cursor, req.Status, req.Status, int(req.Limit)+1)
	var rows []recordsearch.Row
	if err := db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return recordsearch.Page(rows, text, int(req.Limit)), nil
}

func SearchFriends(ctx context.Context, db *gorm.DB, req *search.SearchReq) (*search.SearchResp, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	owner, text := req.OwnerAgentId, req.Query
	pattern, id := recordsearch.Literal(text)
	sql := `SELECT r.id AS key, a.agent_id AS id, COALESCE(NULLIF(r.remark,''),a.agent_name,'') AS title,
   a.agent_name || E'\n' || COALESCE(a.agent_name_en,'') || E'\n' || r.remark AS body, 'friend' AS status, r.created_at AS updated_at,
   0 AS conversation_id, a.agent_id AS peer_id, a.short_id AS short_id
   FROM user_relations r JOIN agents a ON a.agent_id=r.to_uid WHERE r.from_uid=? AND r.rel_type=1 AND
   (a.agent_id=? OR a.short_id=? OR a.agent_name ILIKE ? ESCAPE '!' OR a.agent_name_en ILIKE ? ESCAPE '!' OR r.remark ILIKE ? ESCAPE '!')`
	args := []interface{}{owner, id, text, pattern, pattern, pattern}
	sql = `SELECT * FROM (` + sql + `) matches WHERE (CAST(? AS BIGINT)=0 OR key < ?) AND (?='' OR status=?) ORDER BY key DESC LIMIT ?`
	args = append(args, req.Cursor, req.Cursor, req.Status, req.Status, int(req.Limit)+1)
	var rows []recordsearch.Row
	if err := db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return recordsearch.Page(rows, text, int(req.Limit)), nil
}
