package transport

import (
	"eigenflux_server/kitex_gen/eigenflux/base"
	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/rpc/sort/discovery"
	"encoding/json"
	"errors"
)

func Response(value any, err error) *sortapi.DiscoveryResp {
	r := &sortapi.DiscoveryResp{Payload: "{}", BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}
	if err != nil {
		var de *discovery.Error
		if !errors.As(err, &de) {
			de = &discovery.Error{Code: 503, Reason: "discovery_unavailable"}
		}
		r.BaseResp.Code = int32(de.Code)
		r.BaseResp.Msg = de.Reason
		value = de
	}
	b, e := json.Marshal(value)
	if e != nil {
		r.BaseResp.Code = 500
		r.BaseResp.Msg = "encoding_failed"
	} else {
		r.Payload = string(b)
	}
	return r
}
func DecodeResponse(r *sortapi.DiscoveryResp, err error, dst any) error {
	if err != nil {
		return err
	}
	if r == nil || r.BaseResp == nil {
		return discovery.Failure(503, "discovery_unavailable")
	}
	if r.BaseResp.Code != 0 {
		var de discovery.Error
		if json.Unmarshal([]byte(r.Payload), &de) == nil && de.Code > 0 {
			return &de
		}
		return discovery.Failure(int(r.BaseResp.Code), r.BaseResp.Msg)
	}
	return json.Unmarshal([]byte(r.Payload), dst)
}
