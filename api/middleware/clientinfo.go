package middleware

import (
	"context"
	"strconv"
	"strings"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/hertz/pkg/app"

	"eigenflux_server/pkg/reqinfo"
)

func ClientInfoMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if v := c.GetHeader("X-Skill-Ver"); len(v) > 0 {
			ver := string(v)
			num := parseVersionNum(ver)
			c.Set("skill_ver", ver)
			c.Set("skill_ver_num", num)
			ctx = metainfo.WithPersistentValue(ctx, reqinfo.KeySkillVer, ver)
			ctx = metainfo.WithPersistentValue(ctx, reqinfo.KeySkillVerNum, strconv.Itoa(num))
		}
		if v := c.GetHeader("X-CLI-Ver"); len(v) > 0 {
			ver := string(v)
			num := parseVersionNum(ver)
			c.Set("cli_ver", ver)
			c.Set("cli_ver_num", num)
			ctx = metainfo.WithPersistentValue(ctx, reqinfo.KeyCLIVer, ver)
			ctx = metainfo.WithPersistentValue(ctx, reqinfo.KeyCLIVerNum, strconv.Itoa(num))
		}
		for _, h := range []struct {
			header string
			key    string
			ctxKey string
		}{
			{"X-Client-OS", "client_os", reqinfo.KeyClientOS},
			{"X-Client-TZ", "client_tz", reqinfo.KeyClientTZ},
			{"X-Client-Lang", "client_lang", reqinfo.KeyClientLang},
			{"X-Client-Host", "client_host", reqinfo.KeyClientHost},
			{"X-Client-Channel", "client_channel", reqinfo.KeyClientChannel},
			{"X-Client-ID", "client_id", reqinfo.KeyClientID},
		} {
			if v := c.GetHeader(h.header); len(v) > 0 {
				val := string(v)
				if len(val) > 128 {
					val = val[:128]
				}
				c.Set(h.key, val)
				ctx = metainfo.WithPersistentValue(ctx, h.ctxKey, val)
			}
		}
		c.Next(ctx)
	}
}

func parseVersionNum(ver string) int {
	parts := strings.SplitN(ver, ".", 3)
	if len(parts) != 3 {
		return 0
	}
	x, err1 := strconv.Atoi(parts[0])
	y, err2 := strconv.Atoi(parts[1])
	z, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0
	}
	if y < 0 || y > 99 || z < 0 || z > 99 {
		return 0
	}
	return x*10000 + y*100 + z
}
