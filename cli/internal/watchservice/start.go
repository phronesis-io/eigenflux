package watchservice

import "context"

type NotificationTask struct {
	CLI         string `json:"cli"`
	Home        string `json:"home"`
	Server      string `json:"server"`
	Scope       string `json:"scope"`
	AgentID     string `json:"agent_id"`
	PrincipalID string `json:"principal_id"`
}

func StartNotifications(ctx context.Context, task NotificationTask) (Result, error) {
	return platformStartNotifications(ctx, task)
}
