package compat_test

import (
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/kitex_gen/eigenflux/profile"
	"github.com/cloudwego/gopkg/protocol/thrift"
)

func strptr(value string) *string { return &value }

func encoded(value interface {
	BLength() int
	FastWrite([]byte) int
}) []byte {
	buf := make([]byte, value.BLength())
	return buf[:value.FastWrite(buf)]
}

func TestNewProfileClientDecodesOldAgentWithoutPublicIdentity(t *testing.T) {
	wire := encoded(&profile.Agent{Id: 17, Email: "old@example.test", AgentName: "Old", Bio: "bio", CreatedAt: 1, UpdatedAt: 2})
	var current profile.Agent
	if _, err := current.FastRead(wire); err != nil {
		t.Fatal(err)
	}
	if current.Id != 17 || current.AgentName != "Old" || current.ShortId != nil || current.DisplayName != nil {
		t.Fatalf("new Profile client did not preserve an old response: %#v", current)
	}
}

func TestNewPMClientDecodesOldFriendWithoutPublicIdentity(t *testing.T) {
	wire := encoded(&pm.FriendInfo{AgentId: 23, AgentName: "Peer", FriendSince: 3})
	var current pm.FriendInfo
	if _, err := current.FastRead(wire); err != nil {
		t.Fatal(err)
	}
	if current.AgentId != 23 || current.AgentName != "Peer" || current.ShortId != nil || current.DisplayName != nil {
		t.Fatalf("new PM client did not preserve an old response: %#v", current)
	}
}

func TestOldClientsIgnoreNewPublicIdentityFields(t *testing.T) {
	cases := []struct {
		name string
		wire []byte
		want legacyIdentity
	}{
		{
			name: "Profile Agent",
			wire: encoded(&profile.Agent{Id: 17, Email: "new@example.test", AgentName: "New", Bio: "bio", CreatedAt: 1, UpdatedAt: 2, ShortId: strptr("AbCdE"), DisplayName: strptr("New")}),
			want: legacyIdentity{id: 17, name: "New"},
		},
		{
			name: "PM FriendInfo",
			wire: encoded(&pm.FriendInfo{AgentId: 23, AgentName: "Peer", FriendSince: 3, ShortId: strptr("FgHiJ"), DisplayName: strptr("Peer")}),
			want: legacyIdentity{id: 23, name: "Peer"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := readLegacyIdentity(test.wire)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("legacy decoder got %#v, want %#v", got, test.want)
			}
		})
	}
}

type legacyIdentity struct {
	id   int64
	name string
}

// readLegacyIdentity models the common pre-short-ID prefix shared by the old
// Profile Agent and PM FriendInfo structs. Unknown optional fields are skipped
// exactly as generated Thrift clients do during a rolling deployment.
func readLegacyIdentity(buf []byte) (legacyIdentity, error) {
	var result legacyIdentity
	for offset := 0; ; {
		fieldType, fieldID, size, err := thrift.Binary.ReadFieldBegin(buf[offset:])
		if err != nil {
			return result, err
		}
		offset += size
		if fieldType == thrift.STOP {
			return result, nil
		}
		switch {
		case fieldID == 1 && fieldType == thrift.I64:
			value, read, err := thrift.Binary.ReadI64(buf[offset:])
			if err != nil {
				return result, err
			}
			result.id = value
			offset += read
		case (fieldID == 2 || fieldID == 3) && fieldType == thrift.STRING:
			value, read, err := thrift.Binary.ReadString(buf[offset:])
			if err != nil {
				return result, err
			}
			if fieldID == 2 && result.name == "" {
				result.name = value
			} else if fieldID == 3 {
				result.name = value
			}
			offset += read
		default:
			skipped, err := thrift.Binary.Skip(buf[offset:], fieldType)
			if err != nil {
				return result, err
			}
			offset += skipped
		}
	}
}
