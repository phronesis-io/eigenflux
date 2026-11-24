package dal

import (
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// FriendRequestNote is the original, accepted greeting for a direct friendship.
type FriendRequestNote struct {
	RequestID     int64  `json:"request_id,string"`
	SenderAgentID int64  `json:"sender_agent_id,string"`
	Greeting      string `json:"greeting"`
	CreatedAt     int64  `json:"created_at"`
}

// ConversationOpening scopes the note to a participant's direct friend thread.
// A broadcast conversation between the same two agents has its own history.
func ConversationOpening(db *gorm.DB, viewerID, convID int64) (*FriendRequestNote, error) {
	var note FriendRequestNote
	result := db.Raw(`SELECT n.request_id, n.sender_agent_id, n.greeting, n.created_at
		FROM conversations c
		JOIN friend_conversation_openings n ON n.participant_low = LEAST(c.participant_a, c.participant_b)
			AND n.participant_high = GREATEST(c.participant_a, c.participant_b)
		WHERE c.conv_id = ? AND c.origin_type = 'friend'
		  AND (c.participant_a = ? OR c.participant_b = ?)`, convID, viewerID, viewerID).Scan(&note)
	if result.Error != nil || result.RowsAffected == 0 {
		return nil, result.Error
	}
	return &note, nil
}

type FriendConversation struct {
	PeerID int64
	ConvID int64
	Note   *FriendRequestNote `gorm:"embedded"`
}

// FriendConversations resolves the current contact page, including contacts
// whose accepted request predates their first private message.
func FriendConversations(db *gorm.DB, viewerID int64, peerIDs []int64) (map[int64]FriendConversation, error) {
	rowsByPeer := make(map[int64]FriendConversation, len(peerIDs))
	if len(peerIDs) == 0 {
		return rowsByPeer, nil
	}
	var rows []FriendConversation
	err := db.Raw(`SELECT r.to_uid AS peer_id, c.conv_id, n.request_id, n.sender_agent_id, n.greeting, n.created_at
		FROM user_relations r
		LEFT JOIN friend_conversation_openings n ON n.participant_low = LEAST(r.from_uid, r.to_uid)
			AND n.participant_high = GREATEST(r.from_uid, r.to_uid)
		LEFT JOIN LATERAL (
			SELECT conv_id FROM conversations c
			WHERE c.origin_type = 'friend' AND c.status = 0
			  AND ((c.participant_a = r.from_uid AND c.participant_b = r.to_uid)
			    OR (c.participant_a = r.to_uid AND c.participant_b = r.from_uid))
			ORDER BY c.updated_at DESC, c.conv_id DESC LIMIT 1
		) c ON true
		WHERE r.from_uid = ? AND r.to_uid = ANY(?) AND r.rel_type = 1`, viewerID, pq.Array(peerIDs)).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		rowsByPeer[row.PeerID] = row
	}
	return rowsByPeer, nil
}
