package main

import (
	"context"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/rpc/auth/dal"
	"fmt"
	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/auth"
	shareddb "eigenflux_server/pkg/db"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLegacyLoginRejectsUnknownEmailWithoutRegistration(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err := database.AutoMigrate(&dal.Agent{}, &dal.AgentSession{}, &dal.AuthEmailChallenge{}); err != nil {
		t.Fatal(err)
	}
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousRedis := mq.RDB
	mq.RDB = rdb
	t.Cleanup(func() { mq.RDB = previousRedis; rdb.Close() })
	previous := shareddb.DB
	shareddb.DB = database
	t.Cleanup(func() { shareddb.DB = previous })
	for _, verification := range []bool{false, true} {
		service := &AuthServiceImpl{emailVerificationEnabled: verification}
		started, err := service.StartLogin(context.Background(), &auth.StartLoginReq{LoginMethod: "email", Email: "new@example.com"})
		if err != nil || started.BaseResp.Code != 410 || started.BaseResp.Msg != legacyRegistrationRetired {
			t.Fatalf("start: %+v, %v", started, err)
		}
		completed, err := service.completeEmailLogin(context.Background(), "new@example.com", nil, nil)
		if err != nil || completed.BaseResp.Code != 410 || completed.BaseResp.Msg != legacyRegistrationRetired {
			t.Fatalf("complete: %+v, %v", completed, err)
		}
	}
	var count int64
	if err := database.Table("agents").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("unexpected registration: count=%d error=%v", count, err)
	}
	// V2 identities remain excluded from the legacy path.
	if err := database.Create(&dal.Agent{AgentID: 1, Email: "v2@example.com", EmailKind: "alias"}).Error; err != nil {
		t.Fatal(err)
	}
	response, err := (&AuthServiceImpl{}).StartLogin(context.Background(), &auth.StartLoginReq{LoginMethod: "email", Email: "v2@example.com"})
	if err != nil || response.BaseResp.Code != 400 {
		t.Fatalf("V2 policy: %+v, %v", response, err)
	}
	// A previously issued OTP cannot create a missing identity at verification time.
	code := "123456"
	address := "new@example.com"
	challenge := &dal.AuthEmailChallenge{ChallengeID: "retired", LoginMethod: "email", Email: &address, CodeHash: sha256Hex(code), MaxAttempts: 5, ExpireAt: time.Now().Add(time.Minute).UnixMilli()}
	if err := database.Create(challenge).Error; err != nil {
		t.Fatal(err)
	}
	service := &AuthServiceImpl{emailVerificationEnabled: true}
	verified, err := service.VerifyLogin(context.Background(), &auth.VerifyLoginReq{LoginMethod: "email", ChallengeId: challenge.ChallengeID, Code: &code})
	if err != nil || verified.BaseResp.Code != 410 {
		t.Fatalf("old challenge: %+v, %v", verified, err)
	}
	if err := database.Table("agents").Where("email = ?", address).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("old challenge registered identity: %d, %v", count, err)
	}
	// Both historical representations still support direct and OTP login.
	for index, kind := range []string{"", "legacy_real"} {
		email := fmt.Sprintf("legacy%d@example.com", index)
		agent := &dal.Agent{AgentID: int64(index + 2), Email: email, EmailKind: kind, AgentName: "Legacy", Bio: "Existing account"}
		if err := database.Create(agent).Error; err != nil {
			t.Fatal(err)
		}
		direct, err := (&AuthServiceImpl{}).StartLogin(context.Background(), &auth.StartLoginReq{LoginMethod: "email", Email: email})
		if err != nil || direct.BaseResp.Code != 0 || direct.GetAgentId() != agent.AgentID || direct.GetIsNewAgent() {
			t.Fatalf("legacy direct: %+v, %v", direct, err)
		}
		challenge := &dal.AuthEmailChallenge{ChallengeID: email, LoginMethod: "email", Email: &email, CodeHash: sha256Hex(code), MaxAttempts: 5, ExpireAt: time.Now().Add(time.Minute).UnixMilli()}
		if err := database.Create(challenge).Error; err != nil {
			t.Fatal(err)
		}
		verified, err := service.VerifyLogin(context.Background(), &auth.VerifyLoginReq{LoginMethod: "email", ChallengeId: email, Code: &code})
		if err != nil || verified.BaseResp.Code != 0 || verified.AgentId != agent.AgentID || verified.IsNewAgent {
			t.Fatalf("legacy OTP: %+v, %v", verified, err)
		}
	}

}
