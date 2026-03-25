package middleware

import (
	"context"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

func CommonParamMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if v := c.GetHeader("X-Skill-Ver"); len(v) > 0 {
			ver := string(v)
			c.Set("skill_ver", ver)
			c.Set("skill_ver_num", parseVersionNum(ver))
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
