package authinfo

import (
	"context"
	"strconv"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

const (
	KeyAgentID = "ef.agent_id"
)

type AuthInfo struct {
	AgentID int64
}

func FromContext(ctx context.Context) AuthInfo {
	var a AuthInfo
	if v, ok := metainfo.GetPersistentValue(ctx, KeyAgentID); ok {
		a.AgentID, _ = strconv.ParseInt(v, 10, 64)
	}
	return a
}

func (a AuthInfo) ToVars() map[string]string {
	return map[string]string{
		"agent_id": strconv.FormatInt(a.AgentID, 10),
	}
}
