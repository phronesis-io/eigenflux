package reqinfo

import (
	"context"
	"strconv"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

const (
	KeySkillVer    = "ef.skill_ver"
	KeySkillVerNum = "ef.skill_ver_num"
)

type ClientInfo struct {
	SkillVer    string
	SkillVerNum int
}

func ClientFromContext(ctx context.Context) ClientInfo {
	var c ClientInfo
	if v, ok := metainfo.GetPersistentValue(ctx, KeySkillVer); ok {
		c.SkillVer = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeySkillVerNum); ok {
		c.SkillVerNum, _ = strconv.Atoi(v)
	}
	return c
}

func (c ClientInfo) ToVars() map[string]string {
	return map[string]string{
		"skill_ver":     c.SkillVer,
		"skill_ver_num": strconv.Itoa(c.SkillVerNum),
	}
}
