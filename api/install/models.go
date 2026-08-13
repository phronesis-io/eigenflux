package install

// Token maps to install_tokens. One row per minted invite token. status flips
// pending->installed exactly once on the first /report (the conversion);
// report_count counts every raw report hit (including replays). All UTM and
// referrer data is stored here and recovered by token on report — the token
// itself is an opaque key with no embedded attribution.
type Token struct {
	Token       string `gorm:"column:token;primaryKey"`
	UTMSource   string `gorm:"column:utm_source;not null;default:''"`
	UTMMedium   string `gorm:"column:utm_medium;not null;default:''"`
	UTMCampaign string `gorm:"column:utm_campaign;not null;default:''"`
	UTMContent  string `gorm:"column:utm_content;not null;default:''"`
	UTMTerm     string `gorm:"column:utm_term;not null;default:''"`
	Channel     string `gorm:"column:channel;not null;default:''"`
	Referrer    string `gorm:"column:referrer;type:text;not null;default:''"`
	Status      string `gorm:"column:status;not null;default:'pending'"`
	ReportCount int    `gorm:"column:report_count;not null;default:0"`
	ClientIP    string `gorm:"column:client_ip;not null;default:''"`
	CreatedAt   int64  `gorm:"column:created_at;not null"`
	ReportedAt  int64  `gorm:"column:reported_at;not null;default:0"`
	// FetchedAt is set the first time the agent fetches /r/<ref> (the earliest
	// post-click signal — instructions read but not yet installed).
	FetchedAt int64 `gorm:"column:fetched_at;not null;default:0"`
	// Platform click identifiers captured from the landing URL, kept so a later
	// (cross-device) conversion can be reported back to the ad platform's
	// optimizer keyed by the original click. Each platform has a dedicated field;
	// in particular XingtuClickID is the non-app landing URL's `clickid` and must
	// not be confused with Xiaohongshu ClickID. It maps to the existing
	// xingtu_callback column for schema compatibility; the column now stores the
	// real landing clickid rather than the obsolete server-monitor macro.
	ClickID                       string `gorm:"column:click_id;not null;default:''"`
	Twclid                        string `gorm:"column:twclid;not null;default:''"`
	Gclid                         string `gorm:"column:gclid;not null;default:''"`
	XingtuClickID                 string `gorm:"column:xingtu_callback;type:text;not null;default:''"`
	XingtuCBActivateCode          int    `gorm:"column:xingtu_cb_activate_code;not null;default:-1"`
	XingtuCBActivateSentAt        int64  `gorm:"column:xingtu_cb_activate_sent_at;not null;default:0"`
	XingtuCBRegisterCode          int    `gorm:"column:xingtu_cb_register_code;not null;default:-1"`
	XingtuCBRegisterSentAt        int64  `gorm:"column:xingtu_cb_register_sent_at;not null;default:0"`
	OceanengineClickID            string `gorm:"column:oceanengine_click_id;type:text;not null;default:''"`
	OceanengineAdID               string `gorm:"column:oceanengine_ad_id;not null;default:''"`
	OceanengineCreativeID         string `gorm:"column:oceanengine_creative_id;not null;default:''"`
	OceanengineCreativeType       string `gorm:"column:oceanengine_creative_type;not null;default:''"`
	OceanengineH5FormCode         int    `gorm:"column:oceanengine_h5_form_code;not null;default:-1"`
	OceanengineH5FormSentAt       int64  `gorm:"column:oceanengine_h5_form_sent_at;not null;default:0"`
	OceanengineH5CustomerCode     int    `gorm:"column:oceanengine_h5_customer_code;not null;default:-1"`
	OceanengineH5CustomerSentAt   int64  `gorm:"column:oceanengine_h5_customer_sent_at;not null;default:0"`
	OceanengineOmniFormCode       int    `gorm:"column:oceanengine_omni_form_code;not null;default:-1"`
	OceanengineOmniFormSentAt     int64  `gorm:"column:oceanengine_omni_form_sent_at;not null;default:0"`
	OceanengineOmniCustomerCode   int    `gorm:"column:oceanengine_omni_customer_code;not null;default:-1"`
	OceanengineOmniCustomerSentAt int64  `gorm:"column:oceanengine_omni_customer_sent_at;not null;default:0"`
	// Lang is the entry language the visitor saw on the landing page ('en'/'zh'),
	// for per-language conversion breakdown.
	Lang string `gorm:"column:lang;not null;default:''"`
	// InviteCode is the stable KOL/channel code (EFI-xxxxxx, see pkg/invite) the
	// entry carried, when it came through an invite link rather than a paid ad.
	// The invite code is resolved into this one-shot token at entry time; empty
	// for all non-invite traffic.
	InviteCode string `gorm:"column:invite_code;not null;default:''"`
	// CopiedAt is set the first time the visitor copies the install command on the
	// landing page — the shallow-conversion signal that fires 聚光 event_type 101.
	CopiedAt int64 `gorm:"column:copied_at;not null;default:0"`
	// Two-stage 聚光 ocpx callback state, each exactly-once and independent:
	// cb101 = copy click (event_type 101), cb102 = install success (event_type
	// 102). Code: -1 not attempted, 0 accepted (terminal), >0 platform error,
	// -2 transport error. Non-zero is re-claimable by a later trigger.
	Cb101Code   int   `gorm:"column:cb101_code;not null;default:-1"`
	Cb101SentAt int64 `gorm:"column:cb101_sent_at;not null;default:0"`
	Cb102Code   int   `gorm:"column:cb102_code;not null;default:-1"`
	Cb102SentAt int64 `gorm:"column:cb102_sent_at;not null;default:0"`
	// X Ads CAPI callbacks are tracked independently for the server-confirmed
	// token-created, command-copied, and install-complete funnel stages.
	XCbTokenCode   int   `gorm:"column:x_cb_token_code;not null;default:-1"`
	XCbTokenSentAt int64 `gorm:"column:x_cb_token_sent_at;not null;default:0"`
	XCbCopyCode    int   `gorm:"column:x_cb_copy_code;not null;default:-1"`
	XCbCopySentAt  int64 `gorm:"column:x_cb_copy_sent_at;not null;default:0"`
	XCb102Code     int   `gorm:"column:x_cb102_code;not null;default:-1"`
	XCb102SentAt   int64 `gorm:"column:x_cb102_sent_at;not null;default:0"`
	// Google Ads install-complete offline conversion callback state.
	GoogleAdsCBInstallCode   int   `gorm:"column:google_ads_cb_install_code;not null;default:-1"`
	GoogleAdsCBInstallSentAt int64 `gorm:"column:google_ads_cb_install_sent_at;not null;default:0"`
	// CallbackSentAt / CallbackCode are the legacy single-event callback columns,
	// superseded by the cb101/cb102 pair above; kept for backward compatibility.
	CallbackSentAt int64 `gorm:"column:callback_sent_at;not null;default:0"`
	CallbackCode   int   `gorm:"column:callback_code;not null;default:-1"`
}

func (Token) TableName() string { return "install_tokens" }

const (
	StatusPending   = "pending"
	StatusInstalled = "installed"
)
