package reqinfo

import (
	"context"
	"strconv"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

const (
	KeySkillVer    = "ef.skill_ver"
	KeySkillVerNum = "ef.skill_ver_num"
	KeyCLIVer      = "ef.cli_ver"
	KeyCLIVerNum   = "ef.cli_ver_num"
)

type ClientInfo struct {
	SkillVer    string
	SkillVerNum int
	CLIVer      string
	CLIVerNum   int
}

func ClientFromContext(ctx context.Context) ClientInfo {
	var c ClientInfo
	if v, ok := metainfo.GetPersistentValue(ctx, KeySkillVer); ok {
		c.SkillVer = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeySkillVerNum); ok {
		c.SkillVerNum, _ = strconv.Atoi(v)
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyCLIVer); ok {
		c.CLIVer = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyCLIVerNum); ok {
		c.CLIVerNum, _ = strconv.Atoi(v)
	}
	return c
}

func (c ClientInfo) ToVars() map[string]string {
	return map[string]string{
		"skill_ver":     c.SkillVer,
		"skill_ver_num": strconv.Itoa(c.SkillVerNum),
		"cli_ver":       c.CLIVer,
		"cli_ver_num":   strconv.Itoa(c.CLIVerNum),
	}
}
