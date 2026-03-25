package commonparam

import (
	"context"
	"strconv"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

const (
	KeySkillVer    = "ef.skill_ver"
	KeySkillVerNum = "ef.skill_ver_num"
	KeyAgentID     = "ef.agent_id"
)

type CommonParam struct {
	SkillVer    string
	SkillVerNum int
	AgentID     int64
}

func FromContext(ctx context.Context) CommonParam {
	var p CommonParam
	if v, ok := metainfo.GetPersistentValue(ctx, KeySkillVer); ok {
		p.SkillVer = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeySkillVerNum); ok {
		p.SkillVerNum, _ = strconv.Atoi(v)
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyAgentID); ok {
		p.AgentID, _ = strconv.ParseInt(v, 10, 64)
	}
	return p
}

func (p CommonParam) ToVars() map[string]string {
	return map[string]string{
		"skill_ver":     p.SkillVer,
		"skill_ver_num": strconv.Itoa(p.SkillVerNum),
		"agent_id":      strconv.FormatInt(p.AgentID, 10),
	}
}
