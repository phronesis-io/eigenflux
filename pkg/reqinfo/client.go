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
	KeyClientOS    = "ef.client_os"
	KeyClientTZ    = "ef.client_tz"
	KeyClientLang  = "ef.client_lang"
	KeyClientHost  = "ef.client_host"
	KeyClientChannel  = "ef.client_channel"
	KeyClientID    = "ef.client_id"
)

type ClientInfo struct {
	SkillVer    string
	SkillVerNum int
	CLIVer      string
	CLIVerNum   int
	OS          string
	TZ          string
	Lang        string
	Host        string
	Channel     string
	ClientID    string
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
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientOS); ok {
		c.OS = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientTZ); ok {
		c.TZ = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientLang); ok {
		c.Lang = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientHost); ok {
		c.Host = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientChannel); ok {
		c.Channel = v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, KeyClientID); ok {
		c.ClientID = v
	}
	return c
}

func (c ClientInfo) ToVars() map[string]string {
	return map[string]string{
		"skill_ver":      c.SkillVer,
		"skill_ver_num":  strconv.Itoa(c.SkillVerNum),
		"cli_ver":        c.CLIVer,
		"cli_ver_num":    strconv.Itoa(c.CLIVerNum),
		"client_os":      c.OS,
		"client_tz":      c.TZ,
		"client_lang":    c.Lang,
		"client_host":    c.Host,
		"client_channel": c.Channel,
		"client_id":      c.ClientID,
	}
}
